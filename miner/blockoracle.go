package miner

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/spacemeshos/go-spacemesh/common/types"
	"github.com/spacemeshos/go-spacemesh/log"
	"github.com/spacemeshos/sha256-simd"
	"sync"
)

type activationDB interface {
	GetNodeAtxIDForEpoch(nodeID types.NodeID, targetEpoch types.EpochID) (types.ATXID, error)
	GetAtxHeader(id types.ATXID) (*types.ActivationTxHeader, error)
	GetIdentity(edID string) (types.NodeID, error)
}

type vrfSigner interface {
	Sign(msg []byte) ([]byte, error)
}

// BlockOracle is the oracle that provides block eligibility proofs for the miner.
type BlockOracle struct {
	committeeSize        uint32
	genesisActiveSetSize uint32
	layersPerEpoch       uint16
	atxDB                activationDB
	beaconProvider       *EpochBeaconProvider
	vrfSigner            vrfSigner
	nodeID               types.NodeID

	proofsEpoch       types.EpochID
	eligibilityProofs map[types.LayerID][]types.BlockEligibilityProof
	atxID             types.ATXID
	isSynced          func() bool
	eligibilityMutex  sync.RWMutex
	log               log.Log
}

// NewMinerBlockOracle returns a new BlockOracle.
func NewMinerBlockOracle(committeeSize uint32, genesisActiveSetSize uint32, layersPerEpoch uint16, atxDB activationDB, beaconProvider *EpochBeaconProvider, vrfSigner vrfSigner, nodeID types.NodeID, isSynced func() bool, log log.Log) *BlockOracle {

	return &BlockOracle{
		committeeSize:        committeeSize,
		genesisActiveSetSize: genesisActiveSetSize,
		layersPerEpoch:       layersPerEpoch,
		atxDB:                atxDB,
		beaconProvider:       beaconProvider,
		vrfSigner:            vrfSigner,
		nodeID:               nodeID,
		proofsEpoch:          ^types.EpochID(0),
		isSynced:             isSynced,
		log:                  log,
	}
}

// BlockEligible returns the ATXID and list of block eligibility proofs for the given layer. It caches proofs for a
// single epoch and only refreshes the cache if eligibility is queried for a different epoch.
func (bo *BlockOracle) BlockEligible(layerID types.LayerID) (types.ATXID, []types.BlockEligibilityProof, error) {
	if !bo.isSynced() {
		return types.ATXID{}, nil, fmt.Errorf("cannot calc eligibility, not synced yet")
	}
	epochNumber := layerID.GetEpoch(bo.layersPerEpoch)
	bo.log.Info("asked for eligibility for epoch %d (cached: %d)", epochNumber, bo.proofsEpoch)
	if bo.proofsEpoch != epochNumber {
		err := bo.calcEligibilityProofs(epochNumber)
		if err != nil {
			bo.log.Error("failed to calculate eligibility proofs: %v", err)
			return *types.EmptyATXID, nil, err
		}
	}
	bo.eligibilityMutex.RLock()
	proofs := bo.eligibilityProofs[layerID]
	bo.eligibilityMutex.RUnlock()
	bo.log.Info("miner %v found eligible for %d blocks in layer %d", bo.nodeID.Key[:5], len(proofs), layerID)
	return bo.atxID, proofs, nil
}

func (bo *BlockOracle) calcEligibilityProofs(epochNumber types.EpochID) error {
	bo.log.Info("calculating eligibility")
	epochBeacon := bo.beaconProvider.GetBeacon(epochNumber)

	var activeSetSize uint32
	atx, err := bo.getValidAtxForEpoch(epochNumber)
	if err != nil {
		if !epochNumber.IsGenesis() {
			return fmt.Errorf("failed to get latest ATX: %v", err)
		}
	} else {
		activeSetSize = atx.ActiveSetSize
		bo.atxID = atx.ID()
	}

	if epochNumber.IsGenesis() {
		activeSetSize = bo.genesisActiveSetSize
		bo.log.Info("genesis epoch detected, using GenesisActiveSetSize (%v)", activeSetSize)
	}

	numberOfEligibleBlocks, err := getNumberOfEligibleBlocks(activeSetSize, bo.committeeSize, bo.layersPerEpoch)
	if err != nil {
		bo.log.Error("failed to get number of eligible blocks: %v", err)
		return err
	}

	bo.eligibilityMutex.Lock()
	bo.eligibilityProofs = map[types.LayerID][]types.BlockEligibilityProof{}
	bo.eligibilityMutex.Unlock()
	for counter := uint32(0); counter < numberOfEligibleBlocks; counter++ {
		message := serializeVRFMessage(epochBeacon, epochNumber, counter)
		vrfSig, err := bo.vrfSigner.Sign(message)
		if err != nil {
			bo.log.Error("Could not sign message err=%v", err)
			return err
		}
		vrfHash := sha256.Sum256(vrfSig)
		eligibleLayer := calcEligibleLayer(epochNumber, bo.layersPerEpoch, vrfHash)
		bo.eligibilityMutex.Lock()
		bo.eligibilityProofs[eligibleLayer] = append(bo.eligibilityProofs[eligibleLayer], types.BlockEligibilityProof{
			J:   counter,
			Sig: vrfSig,
		})
		bo.eligibilityMutex.Unlock()
	}
	bo.proofsEpoch = epochNumber
	bo.eligibilityMutex.RLock()
	bo.log.Info("miner %v is eligible for %d blocks on %d layers in epoch %d",
		bo.nodeID.Key[:5], numberOfEligibleBlocks, len(bo.eligibilityProofs), epochNumber)
	bo.eligibilityMutex.RUnlock()
	return nil
}

func (bo *BlockOracle) getValidAtxForEpoch(validForEpoch types.EpochID) (*types.ActivationTxHeader, error) {
	atxID, err := bo.getATXIDForEpoch(validForEpoch)
	if err != nil {
		return nil, fmt.Errorf("failed to get ATX ID for target epoch %v: %v", validForEpoch, err)
	}
	atx, err := bo.atxDB.GetAtxHeader(atxID)
	if err != nil {
		bo.log.Error("getting ATX failed: %v", err)
		return nil, err
	}
	return atx, nil
}

func calcEligibleLayer(epochNumber types.EpochID, layersPerEpoch uint16, vrfHash [32]byte) types.LayerID {
	vrfInteger := binary.LittleEndian.Uint64(vrfHash[:8])
	eligibleLayerOffset := vrfInteger % uint64(layersPerEpoch)
	return epochNumber.FirstLayer(layersPerEpoch).Add(uint16(eligibleLayerOffset))
}

func getNumberOfEligibleBlocks(activeSetSize, committeeSize uint32, layersPerEpoch uint16) (uint32, error) {
	if activeSetSize == 0 {
		return 0, errors.New("empty active set not allowed")
	}
	numberOfEligibleBlocks := committeeSize * uint32(layersPerEpoch) / activeSetSize
	if numberOfEligibleBlocks == 0 {
		numberOfEligibleBlocks = 1
	}
	return numberOfEligibleBlocks, nil
}

func (bo *BlockOracle) getATXIDForEpoch(targetEpoch types.EpochID) (types.ATXID, error) {
	latestATXID, err := bo.atxDB.GetNodeAtxIDForEpoch(bo.nodeID, targetEpoch)
	if err != nil {
		bo.log.With().Info("did not find ATX IDs for node", log.String("atx_node_id", bo.nodeID.ShortString()), log.Err(err))
		return types.ATXID{}, err
	}
	bo.log.With().Info("latest atx id found", log.AtxID(latestATXID.ShortString()))
	return latestATXID, err
}

func serializeVRFMessage(epochBeacon []byte, epochNumber types.EpochID, counter uint32) []byte {
	message := make([]byte, len(epochBeacon)+binary.Size(epochNumber)+binary.Size(counter))
	copy(message, epochBeacon)
	binary.LittleEndian.PutUint64(message[len(epochBeacon):], uint64(epochNumber))
	binary.LittleEndian.PutUint32(message[len(epochBeacon)+binary.Size(epochNumber):], counter)
	return message
}

// GetEligibleLayers returns a list of layers in which the miner is eligible for at least one block. The list is
// returned in arbitrary order.
func (bo *BlockOracle) GetEligibleLayers() []types.LayerID {
	bo.eligibilityMutex.RLock()
	layers := make([]types.LayerID, 0, len(bo.eligibilityProofs))
	for layer := range bo.eligibilityProofs {
		layers = append(layers, layer)
	}
	bo.eligibilityMutex.RUnlock()
	return layers
}
