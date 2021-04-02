/**
* @Author:google btree
* @Date:2021/4/2 下午12:26
* @Desc:
 */

package btre

import (
	"sort"
	"sync"
)

const (
	DefaultFreelistSize = 32 //默认的freelist的大小
)

type FreeList struct {
	mu       sync.Mutex //使用锁保证并发安全
	freelist []*node
}

type Item interface {
	//当前的item是否小于给定的item
	Less(than Item) bool
}

//创建指定大小的freelist
func NewFreeList(size int) *FreeList {
	return &FreeList{freelist: make([]*node, 0, size)}
}

func (f *FreeList) newNode() (n *node) {
	f.mu.Lock()

	//当前freelist的长度
	index := len(f.freelist) - 1
	if index < 0 {
		//当freelist为空的时候，直接new一个node
		f.mu.Unlock()
		return new(node)
	}
	//freelist不为空时，取出freelist中最后一个
	n = f.freelist[index]
	f.freelist[index] = nil
	//更新freelist
	f.freelist = f.freelist[:index]

	f.mu.Unlock()
	return n
}

//将给定的node添加到list中，添加成功返回true，当容量满的时候返回false
func (f *FreeList) freeNode(n *node) (out bool) {
	f.mu.Lock()
	//当前freelist的有空余容量时(当前freelist中的元素数<freelist的容量)
	if len(f.freelist) < cap(f.freelist) {
		f.freelist = append(f.freelist, n)
		out = true
	}
	f.mu.Unlock()

	return out
}

type ItemIterator func(i Item) bool

func New(degree int) *BTree {
	return NewWithFreelist(degree, NewFreeList(DefaultFreelistSize))
}

//BType是B-Tree的一个实现
type BTree struct {
	degree int //度
	length int
	root   *node
	cow    *copyOnWriteContext
}

func NewWithFreelist(degree int, f *FreeList) *BTree {
	if degree <= 1 {
		panic("bad degree")
	}
	return &BTree{
		degree: degree,
		cow:    &copyOnWriteContext{freelist: f},
	}
}

//存储的是存储在一个node中的items
type items []Item

//将一个新的value添加到给定的index中，并把所有的子序列后移
func (i *items) insertAt(index int, item Item) {
	//将nil添加到items中
	*i = append(*i, nil)
	//给定的index在items的范围内时,即已经存在
	if index < len(*i) {
		//当前items的长度>index
		//将(*i)[index:]复制到(*i)[index+1:]中
		copy((*i)[index+1:], (*i)[index:])
	}
	//给定的index不再items的范围内时
	(*i)[index] = item

}

//移除一个给定的index，并把所有子序列前移
func (i *items) removeAt(index int) Item {
	item := (*i)[index]

	copy((*i)[index:], (*i)[index+1:])
	//将最后一个元素设置为nil
	(*i)[len(*i)-1] = nil
	//更新
	*i = (*i)[:len(*i)-1]
	return item
}

//移除并返回list中的最后一个元素
func (i *items) pop() (out Item) {
	index := len(*i) - 1
	out = (*i)[index]
	(*i)[index] = nil
	//更新
	*i = (*i)[:index]
	return out
}

//将index之后的元素清除
func (i *items) truncate(index int) {
	var toClear items
	*i, toClear = (*i)[:index], (*i)[index:]
	for len(toClear) > 0 {
		toClear = toClear[copy(toClear, nilItems):]
	}
}

//todo
//找到给定的item应该在这个list什么位置插入，如果item已经在list中存在，就返回索引index位置和true
func (i items) find(item Item) (index int, found bool) {
	n := sort.Search(len(i), func(n int) bool {
		return item.Less(i[n])
	})
	if n > 0 && !i[n-1].Less(item) {
		return n - 1, true
	}
	return n, false
}

//树的节点
type node struct {
	items    items    //此节点的元素
	children children //此节点包含的子节点
	cow      *copyOnWriteContext
}

//children存储的是在一个node中的子node
type children []*node

func (c *children) insertAt(index int, n *node) {
	*c = append(*c, nil)
	//已经存在
	if index < len(*c) {
		copy((*c)[index+1:], (*c)[index:])
	}
	//不存在
	(*c)[index] = n
}

func (c *children) removeAt(index int) *node {
	n := (*c)[index]
	copy((*c)[index:], (*c)[index+1:])
	(*c)[len(*c)-1] = nil
	//更新
	*c = (*c)[:len(*c)-1]
	return n
}
