package core

import (
	"context"
	"fmt"
	"log"

	retry "github.com/avast/retry-go"
	sdk "github.com/cosmos/cosmos-sdk/types"
	chantypes "github.com/cosmos/ibc-go/v4/modules/core/04-channel/types"
	"golang.org/x/sync/errgroup"
)

// NaiveStrategy is an implementation of Strategy.
type NaiveStrategy struct {
	Ordered      bool
	MaxTxSize    uint64 // maximum permitted size of the msgs in a bundled relay transaction
	MaxMsgLength uint64 // maximum amount of messages in a bundled relay transaction
}

var _ StrategyI = (*NaiveStrategy)(nil)

func NewNaiveStrategy() *NaiveStrategy {
	return &NaiveStrategy{}
}

// GetType implements Strategy
func (st NaiveStrategy) GetType() string {
	return "naive"
}

func (st NaiveStrategy) SetupRelay(ctx context.Context, src, dst *ProvableChain) error {
	if err := src.SetupForRelay(ctx); err != nil {
		return err
	}
	if err := dst.SetupForRelay(ctx); err != nil {
		return err
	}
	return nil
}

func (st NaiveStrategy) UnrelayedSequences(src, dst *ProvableChain, sh SyncHeadersI) (*RelaySequences, error) {
	var (
		eg           = new(errgroup.Group)
		srcPacketSeq = []uint64{}
		dstPacketSeq = []uint64{}
		err          error
		rs           = &RelaySequences{Src: []uint64{}, Dst: []uint64{}}
	)
	srcCtx := sh.GetQueryContext(src.ChainID())
	dstCtx := sh.GetQueryContext(dst.ChainID())

	eg.Go(func() error {
		var res *chantypes.QueryPacketCommitmentsResponse
		if err = retry.Do(func() error {
			// Query the packet commitment
			res, err = src.QueryPacketCommitments(srcCtx, 0, 1000)
			switch {
			case err != nil:
				return err
			case res == nil:
				return fmt.Errorf("no error on QueryPacketCommitments for %s, however response is nil", src.ChainID())
			default:
				return nil
			}
		}, rtyAtt, rtyDel, rtyErr, retry.OnRetry(func(n uint, err error) {
			log.Printf("- [%s]@{%d} - try(%d/%d) query packet commitments: %s", src.ChainID(), srcCtx.Height().GetRevisionHeight(), n+1, rtyAttNum, err)
		})); err != nil {
			return err
		}
		for _, pc := range res.Commitments {
			srcPacketSeq = append(srcPacketSeq, pc.Sequence)
		}
		return nil
	})

	eg.Go(func() error {
		var res *chantypes.QueryPacketCommitmentsResponse
		if err = retry.Do(func() error {
			res, err = dst.QueryPacketCommitments(dstCtx, 0, 1000)
			switch {
			case err != nil:
				return err
			case res == nil:
				return fmt.Errorf("no error on QueryPacketCommitments for %s, however response is nil", dst.ChainID())
			default:
				return nil
			}
		}, rtyAtt, rtyDel, rtyErr, retry.OnRetry(func(n uint, err error) {
			log.Printf("- [%s]@{%d} - try(%d/%d) query packet commitments: %s", dst.ChainID(), dstCtx.Height().GetRevisionHeight(), n+1, rtyAttNum, err)
		})); err != nil {
			return err
		}
		for _, pc := range res.Commitments {
			dstPacketSeq = append(dstPacketSeq, pc.Sequence)
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	eg.Go(func() error {
		// Query all packets sent by src that have been received by dst
		src, err := dst.QueryUnrecievedPackets(dstCtx, srcPacketSeq)
		if err != nil {
			return err
		} else if src != nil {
			rs.Src = src
		}
		return nil
	})

	eg.Go(func() error {
		// Query all packets sent by dst that have been received by src
		dst, err := src.QueryUnrecievedPackets(srcCtx, dstPacketSeq)
		if err != nil {
			return err
		} else if dst != nil {
			rs.Dst = dst
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return rs, nil
}

func (st NaiveStrategy) RelayPackets(src, dst *ProvableChain, sp *RelaySequences, sh SyncHeadersI) error {
	// set the maximum relay transaction constraints
	msgs := &RelayMsgs{
		Src:          []sdk.Msg{},
		Dst:          []sdk.Msg{},
		MaxTxSize:    st.MaxTxSize,
		MaxMsgLength: st.MaxMsgLength,
	}
	addr, err := dst.GetAddress()
	if err != nil {
		return err
	}
	srcCtx := sh.GetQueryProofContext(src.ChainID())
	dstCtx := sh.GetQueryProofContext(dst.ChainID())

	msgs.Dst, err = relayPackets(srcCtx, src, sp.Src, addr)
	if err != nil {
		return err
	}
	addr, err = src.GetAddress()
	if err != nil {
		return err
	}
	msgs.Src, err = relayPackets(dstCtx, dst, sp.Dst, addr)
	if err != nil {
		return err
	}
	if !msgs.Ready() {
		log.Printf("- No packets to relay between [%s]port{%s} and [%s]port{%s}",
			src.ChainID(), src.Path().PortID, dst.ChainID(), dst.Path().PortID)
		return nil
	}

	// Prepend non-empty msg lists with UpdateClient
	if len(msgs.Dst) != 0 {
		// Sending an update from src to dst
		hs, err := sh.SetupHeadersForUpdate(src, dst)
		if err != nil {
			return err
		}
		if len(hs) > 0 {
			addr, err := dst.GetAddress()
			if err != nil {
				return err
			}
			msgs.Dst = append(dst.Path().UpdateClients(hs, addr), msgs.Dst...)
		}
	}

	if len(msgs.Src) != 0 {
		hs, err := sh.SetupHeadersForUpdate(dst, src)
		if err != nil {
			return err
		}
		if len(hs) > 0 {
			addr, err := src.GetAddress()
			if err != nil {
				return err
			}
			msgs.Src = append(src.Path().UpdateClients(hs, addr), msgs.Src...)
		}
	}

	// send messages to their respective chains
	if msgs.Send(src, dst); msgs.Success() {
		if len(msgs.Dst) > 1 {
			logPacketsRelayed(dst, src, len(msgs.Dst)-1)
		}
		if len(msgs.Src) > 1 {
			logPacketsRelayed(src, dst, len(msgs.Src)-1)
		}
	}

	return nil
}

func (st NaiveStrategy) UnrelayedAcknowledgements(src, dst *ProvableChain, sh SyncHeadersI) (*RelaySequences, error) {
	var (
		eg           = new(errgroup.Group)
		srcPacketSeq = []uint64{}
		dstPacketSeq = []uint64{}
		err          error
		rs           = &RelaySequences{Src: []uint64{}, Dst: []uint64{}}
	)

	srcCtx := sh.GetQueryContext(src.ChainID())
	dstCtx := sh.GetQueryContext(dst.ChainID())

	eg.Go(func() error {
		var res *chantypes.QueryPacketAcknowledgementsResponse
		if err = retry.Do(func() error {
			// Query the packet commitment
			res, err = src.QueryPacketAcknowledgementCommitments(srcCtx, 0, 1000)
			switch {
			case err != nil:
				return err
			case res == nil:
				return fmt.Errorf("no error on QueryPacketUnrelayedAcknowledgements for %s, however response is nil", src.ChainID())
			default:
				return nil
			}
		}, rtyAtt, rtyDel, rtyErr, retry.OnRetry(func(n uint, err error) {
			log.Printf("- [%s]@{%d} - try(%d/%d) query packet acknowledgements: %s", src.ChainID(), srcCtx.Height().GetRevisionHeight(), n+1, rtyAttNum, err)
			sh.Updates(src, dst)
		})); err != nil {
			return err
		}
		for _, pc := range res.Acknowledgements {
			srcPacketSeq = append(srcPacketSeq, pc.Sequence)
		}
		return nil
	})

	eg.Go(func() error {
		var res *chantypes.QueryPacketAcknowledgementsResponse
		if err = retry.Do(func() error {
			res, err = dst.QueryPacketAcknowledgementCommitments(dstCtx, 0, 1000)
			switch {
			case err != nil:
				return err
			case res == nil:
				return fmt.Errorf("no error on QueryPacketUnrelayedAcknowledgements for %s, however response is nil", dst.ChainID())
			default:
				return nil
			}
		}, rtyAtt, rtyDel, rtyErr, retry.OnRetry(func(n uint, err error) {
			log.Printf("- [%s]@{%d} - try(%d/%d) query packet acknowledgements: %s", dst.ChainID(), dstCtx.Height().GetRevisionHeight(), n+1, rtyAttNum, err)
			sh.Updates(src, dst)
		})); err != nil {
			return err
		}
		for _, pc := range res.Acknowledgements {
			dstPacketSeq = append(dstPacketSeq, pc.Sequence)
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	eg.Go(func() error {
		// Query all packets sent by src that have been received by dst
		src, err := dst.QueryUnrecievedAcknowledgements(dstCtx, srcPacketSeq)
		// return err
		if err != nil {
			return err
		} else if src != nil {
			rs.Src = src
		}
		return nil
	})

	eg.Go(func() error {
		// Query all packets sent by dst that have been received by src
		dst, err := src.QueryUnrecievedAcknowledgements(srcCtx, dstPacketSeq)
		if err != nil {
			return err
		} else if dst != nil {
			rs.Dst = dst
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return rs, nil
}

// TODO add packet-timeout support
func relayPackets(ctx QueryProofContext, chain *ProvableChain, seqs []uint64, sender sdk.AccAddress) ([]sdk.Msg, error) {
	var msgs []sdk.Msg
	for _, seq := range seqs {
		p, err := chain.QueryPacket(ctx, seq)
		if err != nil {
			log.Println("failed to QueryPacket:", ctx.Height(), seq, err)
			return nil, err
		}
		res, err := chain.QueryPacketCommitmentWithProof(ctx, seq)
		if err != nil {
			log.Println("failed to QueryPacketCommitment:", ctx.Height(), seq, err)
			return nil, err
		}
		msg := chantypes.NewMsgRecvPacket(*p, res.Proof, res.ProofHeight, sender.String())
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

func logPacketsRelayed(src, dst ChainI, num int) {
	log.Printf("★ Relayed %d packets: [%s]port{%s}->[%s]port{%s}",
		num, dst.ChainID(), dst.Path().PortID, src.ChainID(), src.Path().PortID)
}

func (st NaiveStrategy) RelayAcknowledgements(src, dst *ProvableChain, sp *RelaySequences, sh SyncHeadersI) error {
	// set the maximum relay transaction constraints
	msgs := &RelayMsgs{
		Src:          []sdk.Msg{},
		Dst:          []sdk.Msg{},
		MaxTxSize:    st.MaxTxSize,
		MaxMsgLength: st.MaxMsgLength,
	}

	addr, err := dst.GetAddress()
	if err != nil {
		return err
	}
	srcCtx := sh.GetQueryProofContext(src.ChainID())
	dstCtx := sh.GetQueryProofContext(dst.ChainID())

	msgs.Dst, err = relayAcks(dstCtx, srcCtx, dst, src, sp.Src, addr)
	if err != nil {
		return err
	}
	addr, err = src.GetAddress()
	if err != nil {
		return err
	}
	msgs.Src, err = relayAcks(srcCtx, dstCtx, src, dst, sp.Dst, addr)
	if err != nil {
		return err
	}
	if !msgs.Ready() {
		log.Printf("- No acknowledgements to relay between [%s]port{%s} and [%s]port{%s}",
			src.ChainID(), src.Path().PortID, dst.ChainID(), dst.Path().PortID)
		return nil
	}

	// Prepend non-empty msg lists with UpdateClient
	if len(msgs.Dst) != 0 {
		hs, err := sh.SetupHeadersForUpdate(src, dst)
		if err != nil {
			return err
		}
		if len(hs) > 0 {
			addr, err := dst.GetAddress()
			if err != nil {
				return err
			}
			msgs.Dst = append(dst.Path().UpdateClients(hs, addr), msgs.Dst...)
		}
	}

	if len(msgs.Src) != 0 {
		hs, err := sh.SetupHeadersForUpdate(dst, src)
		if err != nil {
			return err
		}
		if len(hs) > 0 {
			addr, err := src.GetAddress()
			if err != nil {
				return err
			}
			msgs.Src = append(src.Path().UpdateClients(hs, addr), msgs.Src...)
		}
	}

	// send messages to their respective chains
	if msgs.Send(src, dst); msgs.Success() {
		if len(msgs.Dst) > 1 {
			logPacketsRelayed(dst, src, len(msgs.Dst)-1)
		}
		if len(msgs.Src) > 1 {
			logPacketsRelayed(src, dst, len(msgs.Src)-1)
		}
	}

	return nil
}

func relayAcks(senderCtx, receriverCtx QueryProofContext, senderChain, receiverChain *ProvableChain, seqs []uint64, sender sdk.AccAddress) ([]sdk.Msg, error) {
	var msgs []sdk.Msg

	for _, seq := range seqs {
		p, err := senderChain.QueryPacket(senderCtx, seq)
		if err != nil {
			return nil, err
		}
		ack, err := receiverChain.QueryPacketAcknowledgement(receriverCtx, seq)
		if err != nil {
			return nil, err
		}
		res, err := receiverChain.QueryPacketAcknowledgementCommitmentWithProof(receriverCtx, seq)
		if err != nil {
			return nil, err
		}

		msg := chantypes.NewMsgAcknowledgement(*p, ack, res.Proof, res.ProofHeight, sender.String())
		msgs = append(msgs, msg)
	}

	return msgs, nil
}
