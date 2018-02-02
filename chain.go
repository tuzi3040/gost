package gost

import (
	"bytes"
	"errors"
	"fmt"
	"net"

	"github.com/go-log/log"
)

var (
	// ErrEmptyChain is an error that implies the chain is empty.
	ErrEmptyChain = errors.New("empty chain")
)

// Chain is a proxy chain that holds a list of proxy nodes.
type Chain struct {
	isRoute    bool
	Retries    int
	nodeGroups []*NodeGroup
}

// NewChain creates a proxy chain with a list of proxy nodes.
func NewChain(nodes ...Node) *Chain {
	chain := &Chain{}
	for _, node := range nodes {
		chain.nodeGroups = append(chain.nodeGroups, NewNodeGroup(node))
	}
	return chain
}

func newRoute(nodes ...Node) *Chain {
	chain := NewChain(nodes...)
	chain.isRoute = true
	return chain
}

// Nodes returns the proxy nodes that the chain holds.
// If a node is a node group, the first node in the group will be returned.
func (c *Chain) Nodes() (nodes []Node) {
	for _, group := range c.nodeGroups {
		if ns := group.Nodes(); len(ns) > 0 {
			nodes = append(nodes, ns[0])
		}
	}
	return
}

// NodeGroups returns the list of node group.
func (c *Chain) NodeGroups() []*NodeGroup {
	return c.nodeGroups
}

// LastNode returns the last node of the node list.
// If the chain is empty, an empty node will be returned.
// If the last node is a node group, the first node in the group will be returned.
func (c *Chain) LastNode() Node {
	if c.IsEmpty() {
		return Node{}
	}
	group := c.nodeGroups[len(c.nodeGroups)-1]
	return group.nodes[0].Clone()
}

// LastNodeGroup returns the last group of the group list.
func (c *Chain) LastNodeGroup() *NodeGroup {
	if c.IsEmpty() {
		return nil
	}
	return c.nodeGroups[len(c.nodeGroups)-1]
}

// AddNode appends the node(s) to the chain.
func (c *Chain) AddNode(nodes ...Node) {
	if c == nil {
		return
	}
	for _, node := range nodes {
		c.nodeGroups = append(c.nodeGroups, NewNodeGroup(node))
	}
}

// AddNodeGroup appends the group(s) to the chain.
func (c *Chain) AddNodeGroup(groups ...*NodeGroup) {
	if c == nil {
		return
	}
	for _, group := range groups {
		c.nodeGroups = append(c.nodeGroups, group)
	}
}

// IsEmpty checks if the chain is empty.
// An empty chain means that there is no proxy node or node group in the chain.
func (c *Chain) IsEmpty() bool {
	return c == nil || len(c.nodeGroups) == 0
}

// Dial connects to the target address addr through the chain.
// If the chain is empty, it will use the net.Dial directly.
func (c *Chain) Dial(addr string) (conn net.Conn, err error) {
	if c.IsEmpty() {
		return net.DialTimeout("tcp", addr, DialTimeout)
	}

	for i := 0; i < c.Retries+1; i++ {
		conn, err = c.dial(addr)
		if err == nil {
			break
		}
	}
	return
}

func (c *Chain) dial(addr string) (net.Conn, error) {
	route, err := c.selectRoute()
	if err != nil {
		return nil, err
	}

	conn, err := route.getConn()
	if err != nil {
		return nil, err
	}

	cc, err := route.LastNode().Client.Connect(conn, addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return cc, nil
}

// Conn obtains a handshaked connection to the last node of the chain.
// If the chain is empty, it returns an ErrEmptyChain error.
func (c *Chain) Conn() (conn net.Conn, err error) {
	for i := 0; i < c.Retries+1; i++ {
		var route *Chain
		route, err = c.selectRoute()
		if err != nil {
			continue
		}
		conn, err = route.getConn()
		if err != nil {
			continue
		}

		break
	}
	return
}

func (c *Chain) getConn() (conn net.Conn, err error) {
	if c.IsEmpty() {
		err = ErrEmptyChain
		return
	}
	nodes := c.Nodes()
	node := nodes[0]

	cn, err := node.Client.Dial(node.Addr, node.DialOptions...)
	if err != nil {
		node.MarkDead()
		return
	}

	cn, err = node.Client.Handshake(cn, node.HandshakeOptions...)
	if err != nil {
		node.MarkDead()
		return
	}
	node.ResetDead()

	preNode := node
	for _, node := range nodes[1:] {
		var cc net.Conn
		cc, err = preNode.Client.Connect(cn, node.Addr)
		if err != nil {
			cn.Close()
			node.MarkDead()
			return
		}
		cc, err = node.Client.Handshake(cc, node.HandshakeOptions...)
		if err != nil {
			cn.Close()
			node.MarkDead()
			return
		}
		node.ResetDead()

		cn = cc
		preNode = node
	}

	conn = cn
	return
}

func (c *Chain) selectRoute() (route *Chain, err error) {
	if c.isRoute {
		return c, nil
	}

	buf := bytes.Buffer{}
	route = newRoute()
	route.Retries = c.Retries

	for _, group := range c.nodeGroups {
		node, err := group.Next()
		if err != nil {
			return nil, err
		}
		buf.WriteString(fmt.Sprintf("%s -> ", node.String()))

		if node.Client.Transporter.Multiplex() {
			node.DialOptions = append(node.DialOptions,
				ChainDialOption(route),
			)
			route = newRoute() // cutoff the chain for multiplex.
			route.Retries = c.Retries
		}

		route.AddNode(node)
	}
	if Debug {
		log.Log("select route:", buf.String())
	}
	return
}
