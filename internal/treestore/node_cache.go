package treestore

import (
	"container/list"
	"sync"
)

// nodeCacheEntries bounds the decoded nodes kept, by their total entries
// (a few hundred bytes each).
const nodeCacheEntries = 200_000

// nodeCache is a least-recently-used cache of decoded tree nodes. A node is
// named by its content, so a cached node never goes stale; it is shared by
// readers and never changed (readNode copies it for writers).
type nodeCache struct {
	mu    sync.Mutex
	max   int
	used  int
	order *list.List
	items map[string]*list.Element
}

type cachedNode struct {
	id   string
	node Node
	cost int
}

func newNodeCache(maxEntries int) *nodeCache {
	return &nodeCache{max: maxEntries, order: list.New(), items: map[string]*list.Element{}}
}

func (c *nodeCache) get(id string) (Node, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[id]
	if !ok {
		return Node{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*cachedNode).node, true
}

func (c *nodeCache) add(id string, node Node) {
	cost := len(node.Entries) + 1
	if cost > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[id]; ok {
		return
	}
	c.items[id] = c.order.PushFront(&cachedNode{id: id, node: node, cost: cost})
	c.used += cost
	for c.used > c.max {
		oldest := c.order.Back()
		evicted := oldest.Value.(*cachedNode)
		c.order.Remove(oldest)
		delete(c.items, evicted.id)
		c.used -= evicted.cost
	}
}
