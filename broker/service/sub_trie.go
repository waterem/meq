package service

import (
	"bytes"
	"encoding/gob"
	"math/rand"
	"sync"

	"github.com/chaingod/talent"

	"github.com/meqio/proto"
	"github.com/weaveworks/mesh"
)

type Node struct {
	ID       uint32
	Subs     map[string][]*SubGroup
	Children map[uint32]*Node
}

type SubTrie struct {
	Roots map[uint32]*Node
}

var (
	wildcard = talent.MurMurHash([]byte{proto.TopicWildcard})
	sublock  = &sync.RWMutex{}
)

func NewSubTrie() *SubTrie {
	return &SubTrie{
		Roots: make(map[uint32]*Node),
	}
}

func (st *SubTrie) Subscribe(topic []byte, queue []byte, cid uint64, addr mesh.PeerName) error {
	t := string(topic)
	tids, err := parseTopic(topic, true)
	if err != nil {
		return err
	}
	rootid := tids[0]
	last := tids[len(tids)-1]

	sublock.RLock()
	root, ok := st.Roots[rootid]
	sublock.RUnlock()

	if !ok {
		root = &Node{
			ID:       rootid,
			Children: make(map[uint32]*Node),
			Subs:     make(map[string][]*SubGroup),
		}
		sublock.Lock()
		st.Roots[rootid] = root
		sublock.Unlock()
	}

	curr := root
	for _, tid := range tids[1:] {
		sublock.RLock()
		child, ok := curr.Children[tid]
		sublock.RUnlock()
		if !ok {
			child = &Node{
				ID:       tid,
				Children: make(map[uint32]*Node),
				Subs:     make(map[string][]*SubGroup),
			}
			sublock.Lock()
			curr.Children[tid] = child
			sublock.Unlock()
		}

		curr = child
		// if encounters the last node in the tree branch, we should add topic to the subs of this node
		if tid == last {
			sublock.RLock()
			t1, ok := curr.Subs[t]
			sublock.RUnlock()
			if !ok {
				// []group
				g := &SubGroup{
					ID: queue,
					Sesses: []Sess{
						Sess{
							Addr: addr,
							Cid:  cid,
						},
					},
				}
				sublock.Lock()
				curr.Subs[t] = []*SubGroup{g}
				sublock.Unlock()
			} else {
				for _, g := range t1 {
					// group already exist,add to group
					if bytes.Compare(g.ID, queue) == 0 {
						g.Sesses = append(g.Sesses, Sess{
							Addr: addr,
							Cid:  cid,
						})
						return nil
					}
				}
				// create group
				g := &SubGroup{
					ID: queue,
					Sesses: []Sess{
						Sess{
							Addr: addr,
							Cid:  cid,
						},
					},
				}
				sublock.Lock()
				curr.Subs[t] = append(curr.Subs[t], g)
				sublock.Unlock()
			}
		}
	}

	return nil
}

func (st *SubTrie) UnSubscribe(topic []byte, group []byte, cid uint64, addr mesh.PeerName) error {
	// t := string(topic)
	// tids, err := parseTopic(topic, true)
	// if err != nil {
	// 	return err
	// }
	// rootid := tids[0]
	// last := tids[len(tids)-1]

	// sublock.RLock()
	// root, ok := st.Roots[rootid]
	// sublock.RUnlock()

	// if !ok {
	// 	return errors.New("no subscribe info")
	// }

	// curr := root
	// for _, tid := range tids[1:] {
	// 	sublock.RLock()
	// 	child, ok := curr.Children[tid]
	// 	sublock.RUnlock()
	// 	if !ok {
	// 		return errors.New("no subscribe info")
	// 	}

	// 	curr = child
	// 	// if encounters the last node in the tree branch, we should remove topic in this node
	// 	if tid == last {
	// 		t1, ok := ms.bk.subs[t]
	// 		if !ok {
	// 			return
	// 		}
	// 		for j, g := range t1 {
	// 			if bytes.Compare(g.ID, group) == 0 {
	// 				// group exist
	// 				for i, c := range g.Sesses {
	// 					if c.Cid == cid && addr == c.Addr {
	// 						// delete sess
	// 						g.Sesses = append(g.Sesses[:i], g.Sesses[i+1:]...)
	// 						if len(g.Sesses) == 0 {
	// 							//delete group
	// 							ms.bk.subs[t] = append(ms.bk.subs[t][:j], ms.bk.subs[t][j+1:]...)
	// 						}
	// 						return
	// 					}
	// 				}
	// 			}
	// 		}
	// 	}
	// }

	return nil
}
func (st *SubTrie) Lookup(topic []byte) ([]Sess, error) {
	tids, err := parseTopic(topic, false)
	if err != nil {
		return nil, err
	}

	var sesses []Sess
	rootid := tids[0]

	sublock.RLock()
	root, ok := st.Roots[rootid]
	sublock.RUnlock()
	if !ok {
		return nil, nil
	}

	// 所有比target长的都应该收到
	// target中的通配符'+'可以匹配任何tid
	// 找到所有路线的最后一个node节点
	var lastNodes []*Node
	if len(tids) == 1 {
		lastNodes = append(lastNodes, root)
	} else {
		st.findLastNodes(root, tids[1:], &lastNodes)
	}

	// 找到lastNode的所有子节点
	sublock.RLock()
	for _, last := range lastNodes {
		st.findSesses(last, &sesses)
	}
	sublock.RUnlock()

	//@todo
	//Remove duplicate elements from the list.
	return sesses, nil
}

func (st *SubTrie) LookupExactly(topic []byte) ([]Sess, error) {
	tids, err := parseTopic(topic, true)
	if err != nil {
		return nil, err
	}

	var sesses []Sess
	rootid := tids[0]

	sublock.RLock()
	defer sublock.RUnlock()
	root, ok := st.Roots[rootid]
	if !ok {
		return nil, nil
	}

	// 所有比target长的都应该收到
	// target中的通配符'+'可以匹配任何tid
	// 找到所有路线的最后一个node节点
	lastNode := root
	for _, tid := range tids[1:] {
		// 任何一个node匹配不到，则认为完全无法匹配
		node, ok := lastNode.Children[tid]
		if !ok {
			return nil, nil
		}

		lastNode = node
	}

	for _, gs := range lastNode.Subs {
		for _, g := range gs {
			s := g.Sesses[rand.Intn(len(g.Sesses))]
			sesses = append(sesses, s)
		}
	}

	return sesses, nil
}

func (st *SubTrie) findSesses(n *Node, sesses *[]Sess) {
	for _, gs := range n.Subs {
		for _, g := range gs {
			s := g.Sesses[rand.Intn(len(g.Sesses))]
			*sesses = append(*sesses, s)
		}
	}

	if len(n.Children) == 0 {
		return
	}
	for _, child := range n.Children {
		st.findSesses(child, sesses)
	}
}

func (st *SubTrie) findLastNodes(n *Node, tids []uint32, nodes *[]*Node) {
	if len(tids) == 1 {
		// 如果只剩一个节点，那就直接查找，不管能否找到，都返回
		node, ok := n.Children[tids[0]]
		if ok {
			*nodes = append(*nodes, node)
		}
		return
	}

	tid := tids[0]
	if tid != wildcard {
		node, ok := n.Children[tid]
		if !ok {
			return
		}
		st.findLastNodes(node, tids[1:], nodes)
	} else {
		for _, node := range n.Children {
			st.findLastNodes(node, tids[1:], nodes)
		}
	}
}

// cluster interface

var _ mesh.GossipData = make(Subs)

// Encode serializes our complete state to a slice of byte-slices.
// In this simple example, we use a single gob-encoded
// buffer: see https://golang.org/pkg/encoding/gob/
func (st *SubTrie) Encode() [][]byte {
	sublock.RLock()
	defer sublock.RUnlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		panic(err)
	}
	msg := make([]byte, len(buf.Bytes())+5)
	msg[4] = CLUSTER_SUBS_SYNC_RESP
	copy(msg[5:], buf.Bytes())
	return [][]byte{msg}
}

// Merge merges the other GossipData into this one,
// and returns our resulting, complete state.
func (st *SubTrie) Merge(osubs mesh.GossipData) (complete mesh.GossipData) {
	return
}