package keeper_test

import (
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/lavanet/lava/testutil/common"
	testkeeper "github.com/lavanet/lava/testutil/keeper"
	"github.com/lavanet/lava/x/pairing/types"
	"github.com/stretchr/testify/require"
)

func TestPairingUniqueness(t *testing.T) {
	servers, keepers, ctx := testkeeper.InitAllKeepers(t)

	//init keepers state
	spec := common.CreateMockSpec()
	keepers.Spec.SetSpec(sdk.UnwrapSDKContext(ctx), spec)

	ctx = testkeeper.AdvanceEpoch(ctx, keepers)

	var balance int64 = 10000
	stake := balance / 10

	consumer1 := common.CreateNewAccount(ctx, *keepers, balance)
	common.StakeAccount(t, ctx, *keepers, *servers, consumer1, spec, stake, false)
	consumer2 := common.CreateNewAccount(ctx, *keepers, balance)
	common.StakeAccount(t, ctx, *keepers, *servers, consumer2, spec, stake, false)

	providers := []common.Account{}
	for i := 1; i <= 1000; i++ {
		provider := common.CreateNewAccount(ctx, *keepers, balance)
		common.StakeAccount(t, ctx, *keepers, *servers, provider, spec, stake, true)
		providers = append(providers, provider)
	}

	ctx = testkeeper.AdvanceEpoch(ctx, keepers)

	providers1, err := keepers.Pairing.GetPairingForClient(sdk.UnwrapSDKContext(ctx), spec.Index, consumer1.Addr)
	require.Nil(t, err)

	providers2, err := keepers.Pairing.GetPairingForClient(sdk.UnwrapSDKContext(ctx), spec.Index, consumer2.Addr)
	require.Nil(t, err)

	require.Equal(t, len(providers1), len(providers2))

	diffrent := false

	for _, provider := range providers1 {
		found := false
		for _, provider2 := range providers2 {
			if provider.Address == provider2.Address {
				found = true
			}
		}
		if !found {
			diffrent = true
		}
	}

	require.True(t, diffrent)

}

// Test that verifies that new get-pairing return values (CurrentEpoch, TimeLeftToNextPairing, SpecLastUpdatedBlock) is working properly
func TestGetPairing(t *testing.T) {
	// BLOCK_TIME = 30sec (testutil/keeper/keepers_init.go)
	constBlockTime := testkeeper.BLOCK_TIME

	// setup testnet with mock spec, stake a client and a provider
	ts := setupForPaymentTest(t)
	// get epochBlocks (number of blocks in an epoch)
	epochBlocks := ts.keepers.Epochstorage.EpochBlocksRaw(sdk.UnwrapSDKContext(ts.ctx))

	// define tests - different epoch, valid tells if the payment request should work
	tests := []struct {
		name                string
		validPairingExists  bool
		isEpochTimesChanged bool
	}{
		{"zeroEpoch", false, false},
		{"firstEpoch", true, false},
		{"commonEpoch", true, false},
		{"epochTimesChanged", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			// Advance an epoch according to the test
			switch tt.name {
			case "zeroEpoch":
				// do nothing
			case "firstEpoch":
				ts.ctx = testkeeper.AdvanceEpoch(ts.ctx, ts.keepers)
			case "commonEpoch":
				for i := 0; i < 5; i++ {
					ts.ctx = testkeeper.AdvanceEpoch(ts.ctx, ts.keepers)
				}
			case "epochTimesChanged":
				for i := 0; i < 5; i++ {
					ts.ctx = testkeeper.AdvanceEpoch(ts.ctx, ts.keepers)
				}
				smallerBlockTime := constBlockTime / 2
				ts.ctx = testkeeper.AdvanceBlocks(ts.ctx, ts.keepers, int(epochBlocks)/2, smallerBlockTime)
				ts.ctx = testkeeper.AdvanceBlocks(ts.ctx, ts.keepers, int(epochBlocks)/2)
			}

			// construct get-pairing request
			pairingReq := types.QueryGetPairingRequest{ChainID: ts.spec.Index, Client: ts.clients[0].address.String()}

			// get pairing for client (for epoch zero there is no pairing -> expect to fail)
			pairing, err := ts.keepers.Pairing.GetPairing(ts.ctx, &pairingReq)
			if !tt.validPairingExists {
				require.NotNil(t, err)
			} else {
				require.Nil(t, err)

				// verify the expected provider
				require.Equal(t, ts.providers[0].address.String(), pairing.Providers[0].Address)

				// verify the current epoch
				currentEpoch := ts.keepers.Epochstorage.GetEpochStart(sdk.UnwrapSDKContext(ts.ctx))
				require.Equal(t, currentEpoch, pairing.CurrentEpoch)

				// verify the SpecLastUpdatedBlock
				specLastUpdatedBlock := ts.spec.BlockLastUpdated
				require.Equal(t, specLastUpdatedBlock, pairing.SpecLastUpdatedBlock)

				// get timestamps from previous epoch
				prevEpoch, err := ts.keepers.Epochstorage.GetPreviousEpochStartForBlock(sdk.UnwrapSDKContext(ts.ctx), currentEpoch)
				require.Nil(t, err)

				// if prevEpoch == 0 -> averageBlockTime = 0, else calculate the time (like the actual get-pairing function)
				averageBlockTime := uint64(0)
				if prevEpoch != 0 {
					// get timestamps
					timestampList := []time.Time{}
					for block := prevEpoch; block <= currentEpoch; block++ {
						blockCore := ts.keepers.BlockStore.LoadBlock(int64(block))
						timestampList = append(timestampList, blockCore.Time)
					}

					// calculate average block time
					totalTime := uint64(0)
					for i := 1; i < len(timestampList); i++ {
						totalTime += uint64(timestampList[i].Sub(timestampList[i-1]).Seconds())
					}
					averageBlockTime = totalTime / epochBlocks
				}

				// Get the next epoch
				nextEpochStart, err := ts.keepers.Epochstorage.GetNextEpoch(sdk.UnwrapSDKContext(ts.ctx), currentEpoch)
				require.Nil(t, err)

				// Get epochBlocksOverlap
				overlapBlocks := ts.keepers.Pairing.EpochBlocksOverlap(sdk.UnwrapSDKContext(ts.ctx))

				// Get number of blocks from the current block to the next epoch
				blocksUntilNewEpoch := nextEpochStart + overlapBlocks - uint64(sdk.UnwrapSDKContext(ts.ctx).BlockHeight())

				// Calculate the time left for the next pairing in seconds (blocks left * avg block time)
				timeLeftToNextPairing := blocksUntilNewEpoch * averageBlockTime

				// verify the TimeLeftToNextPairing
				if !tt.isEpochTimesChanged {
					require.Equal(t, timeLeftToNextPairing, pairing.TimeLeftToNextPairing)
				} else {
					// averageBlockTime in get-pairing query -> minimal average across sampled epoch
					// averageBlockTime in this test -> normal average across epoch
					// we've used a smaller blocktime some of the time -> averageBlockTime from get-pairing is smaller than the averageBlockTime calculated in this test
					require.Less(t, pairing.TimeLeftToNextPairing, timeLeftToNextPairing)
				}
			}

		})
	}
}
