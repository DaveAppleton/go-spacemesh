package api

import (
	"context"

	"github.com/spacemeshos/go-spacemesh/api/pb"
	"github.com/spacemeshos/go-spacemesh/common/types"
	"github.com/spacemeshos/go-spacemesh/common/util"
)

// GetLayer returns the blocks in a layer
func (s SpacemeshGrpcService) GetLayer(ctx context.Context, id *pb.LayerRequest) (resp *pb.LayerResponse, err error) {
	var layer *types.Layer
	var out pb.LayerResponse
	layer, err = s.Tx.GetLayer(types.LayerID(id.Layer))
	if err != nil {
		return
	}
	out.Index = id.Layer
	for _, blok := range layer.Blocks() {
		atxStr := util.Bytes2Hex(blok.ATXID.Bytes())
		dataStr := util.Bytes2Hex(blok.Data)
		//pb.Atx{AtxID: }
		thisBlock := pb.Block{LayerID: blok.LayerIndex.Uint64(), Timestamp: blok.Timestamp, AtxID: atxStr, Data: dataStr}
		for _, tx := range blok.TxIDs {
			thisBlock.TxIDs = append(thisBlock.TxIDs, util.Bytes2Hex(tx.Bytes()))
		}
		for _, atx := range blok.ATXIDs {
			thisBlock.AtxIDs = append(thisBlock.AtxIDs, util.Bytes2Hex(atx.Bytes()))
		}
		out.Blocks = append(out.Blocks, &thisBlock)
	}
	return &out, nil
}
