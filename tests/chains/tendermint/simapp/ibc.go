package simapp

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	storetypes "github.com/cosmos/cosmos-sdk/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	clientkeeper "github.com/cosmos/ibc-go/v7/modules/core/02-client/keeper"
	connectionkeeper "github.com/cosmos/ibc-go/v7/modules/core/03-connection/keeper"
	connectiontypes "github.com/cosmos/ibc-go/v7/modules/core/03-connection/types"
	channeltypes "github.com/cosmos/ibc-go/v7/modules/core/04-channel/types"
	"github.com/cosmos/ibc-go/v7/modules/core/exported"
	ibckeeper "github.com/cosmos/ibc-go/v7/modules/core/keeper"
	tenderminttypes "github.com/cosmos/ibc-go/v7/modules/light-clients/07-tendermint"
	mocktypes "github.com/datachainlab/ibc-mock-client/modules/light-clients/xx-mock/types"
)

func overrideIBCClientKeeper(k ibckeeper.Keeper, cdc codec.BinaryCodec, key storetypes.StoreKey, paramSpace paramtypes.Subspace) *ibckeeper.Keeper {
	clientKeeper := NewClientKeeper(k.ClientKeeper)
	k.ConnectionKeeper = connectionkeeper.NewKeeper(cdc, key, paramSpace, clientKeeper)
	return &k
}

var _ connectiontypes.ClientKeeper = (*ClientKeeper)(nil)
var _ channeltypes.ClientKeeper = (*ClientKeeper)(nil)

// ClientKeeper override `ValidateSelfClient` in the keeper of ibc-client
// original method doesn't yet support a consensus state for general client
type ClientKeeper struct {
	clientkeeper.Keeper
}

func NewClientKeeper(k clientkeeper.Keeper) ClientKeeper {
	return ClientKeeper{Keeper: k}
}

func (k ClientKeeper) ValidateSelfClient(ctx sdk.Context, clientState exported.ClientState) error {
	switch cs := clientState.(type) {
	case *tenderminttypes.ClientState:
		return k.Keeper.ValidateSelfClient(ctx, cs)
	case *mocktypes.ClientState:
		return nil
	default:
		return fmt.Errorf("unexpected client state type: %T", cs)
	}
}
