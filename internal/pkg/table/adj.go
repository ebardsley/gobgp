// Copyright (C) 2015 Nippon Telegraph and Telephone Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package table

import (
	"fmt"

	"github.com/ebardsley/gobgp/pkg/packet/bgp"
)

type AdjRib struct {
	accepted map[bgp.RouteFamily]int
	table    map[bgp.RouteFamily]*Table
}

func NewAdjRib(rfList []bgp.RouteFamily) *AdjRib {
	m := make(map[bgp.RouteFamily]*Table)
	for _, f := range rfList {
		m[f] = NewTable(f)
	}
	return &AdjRib{
		table:    m,
		accepted: make(map[bgp.RouteFamily]int),
	}
}

func (adj *AdjRib) Update(pathList []*Path) {
	for _, path := range pathList {
		if path == nil || path.IsEOR() {
			continue
		}
		rf := path.GetRouteFamily()
		t := adj.table[path.GetRouteFamily()]
		d := t.getOrCreateDest(path.GetNlri(), 0)
		var old *Path
		idx := -1
		for i, p := range d.knownPathList {
			if p.GetNlri().PathIdentifier() == path.GetNlri().PathIdentifier() {
				idx = i
				break
			}
		}
		if idx != -1 {
			old = d.knownPathList[idx]
		}

		if path.IsWithdraw {
			if idx != -1 {
				d.knownPathList = append(d.knownPathList[:idx], d.knownPathList[idx+1:]...)
				if len(d.knownPathList) == 0 {
					t.deleteDest(d)
				}
				if !old.IsAsLooped() {
					adj.accepted[rf]--
				}
			}
			path.SetDropped(true)
		} else {
			if idx != -1 {
				if old.IsAsLooped() && !path.IsAsLooped() {
					adj.accepted[rf]++
				} else if !old.IsAsLooped() && path.IsAsLooped() {
					adj.accepted[rf]--
				}
				if old.Equal(path) {
					path.setTimestamp(old.GetTimestamp())
				}
				d.knownPathList[idx] = path
			} else {
				d.knownPathList = append(d.knownPathList, path)
				if !path.IsAsLooped() {
					adj.accepted[rf]++
				}
			}
		}
	}
}

func (adj *AdjRib) walk(families []bgp.RouteFamily, fn func(*Destination) bool) {
	for _, f := range families {
		if t, ok := adj.table[f]; ok {
			for _, d := range t.destinations {
				if fn(d) {
					return
				}
			}
		}
	}
}

func (adj *AdjRib) PathList(rfList []bgp.RouteFamily, accepted bool) []*Path {
	pathList := make([]*Path, 0, adj.Count(rfList))
	adj.walk(rfList, func(d *Destination) bool {
		for _, p := range d.knownPathList {
			if accepted && p.IsAsLooped() {
				continue
			}
			pathList = append(pathList, p)
		}
		return false
	})
	return pathList
}

func (adj *AdjRib) Count(rfList []bgp.RouteFamily) int {
	count := 0
	adj.walk(rfList, func(d *Destination) bool {
		count += len(d.knownPathList)
		return false
	})
	return count
}

func (adj *AdjRib) Accepted(rfList []bgp.RouteFamily) int {
	count := 0
	for _, rf := range rfList {
		if n, ok := adj.accepted[rf]; ok {
			count += n
		}
	}
	return count
}

func (adj *AdjRib) Drop(rfList []bgp.RouteFamily) []*Path {
	l := make([]*Path, 0, adj.Count(rfList))
	adj.walk(rfList, func(d *Destination) bool {
		for _, p := range d.knownPathList {
			w := p.Clone(true)
			w.SetDropped(true)
			l = append(l, w)
		}
		return false
	})
	for _, rf := range rfList {
		adj.table[rf] = NewTable(rf)
		adj.accepted[rf] = 0
	}
	return l
}

func (adj *AdjRib) DropStale(rfList []bgp.RouteFamily) []*Path {
	pathList := make([]*Path, 0, adj.Count(rfList))
	adj.walk(rfList, func(d *Destination) bool {
		for _, p := range d.knownPathList {
			if p.IsStale() {
				w := p.Clone(true)
				w.SetDropped(true)
				pathList = append(pathList, w)
			}
		}
		return false
	})
	adj.Update(pathList)
	return pathList
}

func (adj *AdjRib) StaleAll(rfList []bgp.RouteFamily) []*Path {
	pathList := make([]*Path, 0, adj.Count(rfList))
	adj.walk(rfList, func(d *Destination) bool {
		for i, p := range d.knownPathList {
			n := p.Clone(false)
			n.MarkStale(true)
			n.SetAsLooped(p.IsAsLooped())
			d.knownPathList[i] = n
			if !n.IsAsLooped() {
				pathList = append(pathList, n)
			}
		}
		return false
	})
	return pathList
}

func (adj *AdjRib) MarkLLGRStaleOrDrop(rfList []bgp.RouteFamily) []*Path {
	pathList := make([]*Path, 0, adj.Count(rfList))
	adj.walk(rfList, func(d *Destination) bool {
		for i, p := range d.knownPathList {
			if p.HasNoLLGR() {
				n := p.Clone(true)
				n.SetDropped(true)
				pathList = append(pathList, n)
			} else {
				n := p.Clone(false)
				n.SetAsLooped(p.IsAsLooped())
				n.SetCommunities([]uint32{uint32(bgp.COMMUNITY_LLGR_STALE)}, false)
				if p.IsAsLooped() {
					d.knownPathList[i] = n
				} else {
					pathList = append(pathList, n)
				}
			}
		}
		return false
	})
	adj.Update(pathList)
	return pathList
}

func (adj *AdjRib) Select(family bgp.RouteFamily, accepted bool, option ...TableSelectOption) (*Table, error) {
	t, ok := adj.table[family]
	if !ok {
		t = NewTable((family))
	}
	option = append(option, TableSelectOption{adj: true})
	return t.Select(option...)
}

func (adj *AdjRib) TableInfo(family bgp.RouteFamily) (*TableInfo, error) {
	if _, ok := adj.table[family]; !ok {
		return nil, fmt.Errorf("%s unsupported", family)
	}
	c := adj.Count([]bgp.RouteFamily{family})
	a := adj.Accepted([]bgp.RouteFamily{family})
	return &TableInfo{
		NumDestination: c,
		NumPath:        c,
		NumAccepted:    a,
	}, nil
}
