package cmd

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/hyperledger-labs/yui-relayer/config"
	"github.com/spf13/cobra"
	sdk "github.com/cosmos/cosmos-sdk/types"
	vsc "github.com/vsc-blockchain/core/types"
)

func TendermintCmd(m codec.Codec, ctx *config.Context) *cobra.Command {
	sdk.DefaultPowerReduction = vsc.PowerReduction

	cmd := &cobra.Command{
		Use:   "tendermint",
		Short: "manage tendermint configurations",
	}

	cmd.AddCommand(
		configCmd(m),
		keysCmd(ctx),
		lightCmd(ctx),
	)

	return cmd
}
