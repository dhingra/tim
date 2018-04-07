// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package light

import (
	"bytes"
	"context"

	"github.com/tim-coin/tim/common"
	"github.com/tim-coin/tim/core"
	"github.com/tim-coin/tim/core/types"
	"github.com/tim-coin/tim/crypto"
	"github.com/tim-coin/tim/rlp"
)

var sha3_nil = crypto.Keccak256Hash(nil)

func GetHeaderByNumber(ctx context.Context, odr OdrBackend, number uint64) (*types.Header, error) {
	db := odr.Database()
	hash := core.GetCanonicalHash(db, number)
	if (hash != common.Hash{}) {
		// if there is a canonical hash, there is a header too
		header := core.GetHeader(db, hash, number)
		if header == nil {
			panic("Canonical hash present but header not found")
		}
		return header, nil
	}

	var (
		chtCount, sectionHeadNum uint64
		sectionHead              common.Hash
	)
	if odr.ChtIndexer() != nil {
		chtCount, sectionHeadNum, sectionHead = odr.ChtIndexer().Sections()
		canonicalHash := core.GetCanonicalHash(db, sectionHeadNum)
		// if the CHT was injected as a trusted checkpoint, we have no canonical hash yet so we accept zero hash too
		for chtCount > 0 && canonicalHash != sectionHead && canonicalHash != (common.Hash{}) {
			chtCount--
			if chtCount > 0 {
				sectionHeadNum = chtCount*ChtFrequency - 1
				sectionHead = odr.ChtIndexer().SectionHead(chtCount - 1)
				canonicalHash = core.GetCanonicalHash(db, sectionHeadNum)
			}
		}
	}

	if number >= chtCount*ChtFrequency {
		return nil, ErrNoTrustedCht
	}

	r := &ChtRequest{ChtRoot: GetChtRoot(db, chtCount-1, sectionHead), ChtNum: chtCount - 1, BlockNum: number}
	if err := odr.Retrieve(ctx, r); err != nil {
		return nil, err
	} else {
		return r.Header, nil
	}
}

func GetCanonicalHash(ctx context.Context, odr OdrBackend, number uint64) (common.Hash, error) {
	hash := core.GetCanonicalHash(odr.Database(), number)
	if (hash != common.Hash{}) {
		return hash, nil
	}
	header, err := GetHeaderByNumber(ctx, odr, number)
	if header != nil {
		return header.Hash(), nil
	}
	return common.Hash{}, err
}

// GetBodyRLP retrieves the block body (transactions and uncles) in RLP encoding.
func GetBodyRLP(ctx context.Context, odr OdrBackend, hash common.Hash, number uint64) (rlp.RawValue, error) {
	if data := core.GetBodyRLP(odr.Database(), hash, number); data != nil {
		return data, nil
	}
	r := &BlockRequest{Hash: hash, Number: number}
	if err := odr.Retrieve(ctx, r); err != nil {
		return nil, err
	} else {
		return r.Rlp, nil
	}
}

// GetBody retrieves the block body (transactons, uncles) corresponding to the
// hash.
func GetBody(ctx context.Context, odr OdrBackend, hash common.Hash, number uint64) (*types.Body, error) {
	data, err := GetBodyRLP(ctx, odr, hash, number)
	if err != nil {
		return nil, err
	}
	body := new(types.Body)
	if err := rlp.Decode(bytes.NewReader(data), body); err != nil {
		return nil, err
	}
	return body, nil
}

// GetBlock retrieves an entire block corresponding to the hash, assembling it
// back from the stored header and body.
func GetBlock(ctx context.Context, odr OdrBackend, hash common.Hash, number uint64) (*types.Block, error) {
	// Retrieve the block header and body contents
	header := core.GetHeader(odr.Database(), hash, number)
	if header == nil {
		return nil, ErrNoHeader
	}
	body, err := GetBody(ctx, odr, hash, number)
	if err != nil {
		return nil, err
	}
	// Reassemble the block and return
	return types.NewBlockWithHeader(header).WithBody(body.Transactions, body.Uncles), nil
}

// GetBlockReceipts retrieves the receipts generated by the transactions included
// in a block given by its hash.
func GetBlockReceipts(ctx context.Context, odr OdrBackend, hash common.Hash, number uint64) (types.Receipts, error) {
	receipts := core.GetBlockReceipts(odr.Database(), hash, number)
	if receipts != nil {
		return receipts, nil
	}
	r := &ReceiptsRequest{Hash: hash, Number: number}
	if err := odr.Retrieve(ctx, r); err != nil {
		return nil, err
	}
	return r.Receipts, nil
}

// GetBloomBits retrieves a batch of compressed bloomBits vectors belonging to the given bit index and section indexes
func GetBloomBits(ctx context.Context, odr OdrBackend, bitIdx uint, sectionIdxList []uint64) ([][]byte, error) {
	db := odr.Database()
	result := make([][]byte, len(sectionIdxList))
	var (
		reqList []uint64
		reqIdx  []int
	)

	var (
		bloomTrieCount, sectionHeadNum uint64
		sectionHead                    common.Hash
	)
	if odr.BloomTrieIndexer() != nil {
		bloomTrieCount, sectionHeadNum, sectionHead = odr.BloomTrieIndexer().Sections()
		canonicalHash := core.GetCanonicalHash(db, sectionHeadNum)
		// if the BloomTrie was injected as a trusted checkpoint, we have no canonical hash yet so we accept zero hash too
		for bloomTrieCount > 0 && canonicalHash != sectionHead && canonicalHash != (common.Hash{}) {
			bloomTrieCount--
			if bloomTrieCount > 0 {
				sectionHeadNum = bloomTrieCount*BloomTrieFrequency - 1
				sectionHead = odr.BloomTrieIndexer().SectionHead(bloomTrieCount - 1)
				canonicalHash = core.GetCanonicalHash(db, sectionHeadNum)
			}
		}
	}

	for i, sectionIdx := range sectionIdxList {
		sectionHead := core.GetCanonicalHash(db, (sectionIdx+1)*BloomTrieFrequency-1)
		// if we don't have the canonical hash stored for this section head number, we'll still look for
		// an entry with a zero sectionHead (we store it with zero section head too if we don't know it
		// at the time of the retrieval)
		bloomBits, err := core.GetBloomBits(db, bitIdx, sectionIdx, sectionHead)
		if err == nil {
			result[i] = bloomBits
		} else {
			if sectionIdx >= bloomTrieCount {
				return nil, ErrNoTrustedBloomTrie
			}
			reqList = append(reqList, sectionIdx)
			reqIdx = append(reqIdx, i)
		}
	}
	if reqList == nil {
		return result, nil
	}

	r := &BloomRequest{BloomTrieRoot: GetBloomTrieRoot(db, bloomTrieCount-1, sectionHead), BloomTrieNum: bloomTrieCount - 1, BitIdx: bitIdx, SectionIdxList: reqList}
	if err := odr.Retrieve(ctx, r); err != nil {
		return nil, err
	} else {
		for i, idx := range reqIdx {
			result[idx] = r.BloomBits[i]
		}
		return result, nil
	}
}
