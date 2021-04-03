/**
* @Author:google btree
* @Date:2021/4/2 下午12:26
* @Desc:
 */

package btre

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

const (
	DefaultFreelistSize = 32 //默认的freelist的大小
)

var (
	nilItems    = make(items, 16)
	nilChildren = make(children, 16)
)

type FreeList struct {
	mu       sync.Mutex //使用锁保证并发安全
	freelist []*node    //空闲链表
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

//根据给定degree来生成一个空闲链表
func New(degree int) *BTree {
	return NewWithFreelist(degree, NewFreeList(DefaultFreelistSize))
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

type Item interface {
	//当前的item是否小于给定的item
	Less(than Item) bool
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

//找到给定的item应该在这个list什么位置插入，如果item已经在list中存在，就返回索引index位置和true
func (i items) find(item Item) (index int, found bool) {
	n := sort.Search(len(i), func(n int) bool {
		return item.Less(i[n])
	})
	//已经存在
	if n > 0 && !i[n-1].Less(item) {
		return n - 1, true
	}
	return n, false
}

//children存储的是在一个node中的子node
type children []*node

//在children中的指定位置插入node
func (c *children) insertAt(index int, n *node) {
	*c = append(*c, nil)
	//已经存在
	if index < len(*c) {
		copy((*c)[index+1:], (*c)[index:])
	}
	//不存在
	(*c)[index] = n
}

//移除给定元素
func (c *children) removeAt(index int) *node {
	n := (*c)[index]
	copy((*c)[index:], (*c)[index+1:])
	//最后一位已经前移,所以需要设置为nil
	(*c)[len(*c)-1] = nil
	//更新
	*c = (*c)[:len(*c)-1]
	return n
}

//移除最后一个元素并返回
func (c *children) pop() (out *node) {
	index := len(*c) - 1
	out = (*c)[index]
	//将最后一个元素设置为nil
	(*c)[index] = nil
	//缩小切片的范围，切片是左闭右开
	*c = (*c)[:index]
	return out
}

//截断
func (c *children) truncate(index int) {
	var toClear children
	//将(*c)[index:]从c中去掉
	*c, toClear = (*c)[:index], (*c)[index:]
	//将被截去的部分设置为nilChildren
	for len(toClear) > 0 {
		//copy的返回值是复制的长度
		//nilChildren的长度为16，一次复制不完，会继续复制，直到toClear的长度为0
		toClear = toClear[copy(toClear, nilChildren):]
	}
}

//树的节点
type node struct {
	items    items    //此节点的元素
	children children //此节点包含的子节点
	cow      *copyOnWriteContext
}

//  copyOnWriteContext指针确定节点的所有权...具有与节点的写入上下文等效的写入上下文的树可用于修改该节点。
//  不允许修改其写上下文与节点不匹配的树，并且必须创建一个新的可写副本（即，它是克隆）。 在执行任何写操作时，我们保持不变，即当前节点的上下文等于请求写入的树的上下文。
//  为此，我们需要在上下文不匹配的情况下，通过使用正确的上下文创建一个副本，然后再进入任何节点。
//  由于我们当前在任何写操作中访问的节点都具有请求树的上下文，因此该节点可以在适当的位置进行修改。 该节点的子节点可能不会共享上下文，但是在我们进入它们之前，我们将创建一个可变的副本。
type copyOnWriteContext struct {
	freelist *FreeList
}

//可变的
func (n *node) mutableFor(cow *copyOnWriteContext) *node {
	//如果node的copyOnWriteContext和指定的copyOnWriteContext相等
	if n.cow == cow {
		//返回当前节点
		return n
	}
	//生成copyOnWriteContext的一个新node
	out := cow.newNode()
	if cap(out.items) >= len(n.items) {
		//缩小
		out.items = out.items[:len(n.items)]
	} else {
		//当cow的新节点的长度 < n的cow的长度的时候，创建的长度和容量为len(n.items)
		out.items = make(items, len(n.items), cap(n.items))
	}
	copy(out.items, n.items)
	//复制子节点
	if cap(out.children) >= len(n.children) {
		out.children = out.children[:len(n.children)]
	} else {
		out.children = make(children, len(n.children), cap(n.children))
	}
	copy(out.children, n.children)
	return out
}

//todo 根据给定子节点-》可变
func (n *node) mutableChild(i int) *node {
	c := n.children[i].mutableFor(n.cow)
	n.children[i] = c
	return c
}

//将i之后的item和node，清除掉
func (n *node) split(i int) (Item, *node) {
	item := n.items[i]
	//生成copyOnWriteContext的一个新node
	next := n.cow.newNode()
	//将i之后的所有items加入到新生成的node的items中
	next.items = append(next.items, n.items[i+1:]...)
	//将items中i之后的item都删除
	n.items.truncate(i)
	//处理n.children中的*node
	if len(n.children) > 0 {
		//将n.children的i之后的所有*node都加入到next.children中
		next.children = append(next.children, n.children[i+1:]...)
		n.children.truncate(i + 1)
	}
	return item, next
}

//是否分裂child
func (n *node) maybeSplitChild(i, maxItems int) bool {
	//小于给定的maxItems
	if len(n.children[i].items) < maxItems {
		//不分裂
		return false
	}
	//大于maxItems,则分裂
	first := n.mutableChild(i)
	item, second := first.split(maxItems / 2)
	//在给定位置插入item
	n.items.insertAt(i, item)
	//插入的child中
	n.children.insertAt(i+1, second)
	return true
}

//在以此节点为根节点的子树上插入item，并且确保没有节点超出子树的maxItems
//如果要插入的item已经存在，就把它返回
func (n *node) insert(item Item, maxItems int) Item {
	//i为item的位置
	i, found := n.items.find(item)
	if found {
		out := n.items[i]
		//更新
		n.items[i] = item
		return out
	}
	//不存在
	//当前节点的子节点是空的
	if len(n.children) == 0 {
		//在给定的位置插入item
		n.items.insertAt(i, item)
	}
	//拆分child
	if n.maybeSplitChild(i, maxItems) {
		inTree := n.items[i]
		switch {
		case item.Less(inTree):
			//不做任何更改，我们只需要第一个拆分的node
		case inTree.Less(item):
			//我们需要的是第二个拆分的node
			i++
		default:
			out := n.items[i]
			n.items[i] = item
			return out
		}

	}
	return n.mutableChild(i).insert(item, maxItems)
}

//在子树中找到key
func (n *node) get(key Item) Item {
	i, found := n.items.find(key)
	if found {
		return n.items[i]
	} else if len(n.children) > 0 {
		//子节点不为空，去i的子树中查找
		return n.children[i].get(key)
	}
	//没有找到
	return nil
}

//返回子树中第一个item
func min(n *node) Item {
	if n == nil {
		return nil
	}
	for len(n.children) > 0 {
		//直到叶子节点为止，即children为空
		//返回叶子节点的第一个
		n = n.children[0]
	}
	//为空
	if len(n.items) == 0 {
		return nil
	}
	//返回叶子节点中第一个tem
	return n.items[0]
}

//返回子树中最后一个item
func max(n *node) Item {
	if n == nil {
		return nil
	}
	//到达叶子节点
	for len(n.children) > 0 {
		n = n.children[len(n.children)-1]
	}
	if len(n.items) == 0 {
		return nil
	}
	return n.items[len(n.items)-1]
}

type toRemove int

const (
	removeItem toRemove = iota //移除给定的item
	removeMin                  //移除子树中最小的item
	removeMax                  //移除子树中最大的item
)

//移除node中的item
func (n *node) remove(item Item, minItems int, typ toRemove) Item {
	var i int
	var found bool
	switch typ {
	case removeMax:
		//移除子树中最大的item
		if len(n.children) == 0 {
			//子树为空，todo
			return n.items.pop()
		}
		//high
		i = len(n.items)
	case removeMin:
		//移除子树中最小的item
		if len(n.children) == 0 {
			return n.items.removeAt(0)
		}
		//low
		i = 0
	case removeItem:
		i, found = n.items.find(item)
		if len(n.children) == 0 {
			if found {
				return n.items.removeAt(i)
			}
			return nil
		}
	default:
		panic("invalid type")
	}

	//以下children不为空
	if len(n.children[i].items) <= minItems {
		//小于给定的minItems,则扩大
		return n.growChildAndRemove(i, item, minItems, typ)
	}
	child := n.mutableChild(i)
	//要么我们有足够的items，或者做了一些merging/stealing,因为我们已经足够的items了，所以可以准备return stuff了
	if found {
		//item存在的位置是i，
		//todo
		// The item exists at index 'i', and the child we've selected can give us a
		// predecessor, since if we've gotten here it's got > minItems items in it.
		out := n.items[i]
		// We use our special-case 'remove' call with typ=maxItem to pull the
		// predecessor of item i (the rightmost leaf of our immediate left child)
		// and set it into where we pulled the item from.
		n.items[i] = child.remove(nil, minItems, removeMax)
		return out
	}
	//一旦我们到了这个位置的时候，我们知道这个item不在node中，而且 the child 应该去移除因为已经足够大了
	//递归调用
	return child.remove(item, minItems, typ)

}

// growChildAndRemove增长子对象“ i”，以确保可以在将其保留在minItems的同时将其从item中删除，然后调用remove实际将其删除。
//大多数文档说我们必须处理两种特殊情况：
// 1）item在此节点中
// 2）item在子项中
//在两种情况下，我们都需要处理两个子情况：
// A）节点具有足够的值，因此可以保留一个
// B）节点的值不足
//对于后者，我们必须检查：
// a）左兄弟姐妹有剩余节点
// b）兄弟姐妹有剩余节点
// c）我们必须合并
//为了简化代码，我们将情况＃1和＃2以相同的方式处理：
//如果节点没有足够的item，则确保它有（使用a，b，c）。
//然后，我们只是简单地重做移除调用，然后第二次（无论我们是在案例1还是案例2中），我们将有足够的项目并可以保证我们碰到案例A。
func (n *node) growChildAndRemove(i int, item Item, minItems int, typ toRemove) Item {
	if i > 0 && len(n.children[i-1].items) > minItems {
		//从左子节点窃取
		child := n.mutableChild(i)
		stealFrom := n.mutableChild(i - 1)
		stolenItem := stealFrom.items.pop()
		//在给定的位置插入
		child.items.insertAt(0, n.items[i-1])
		n.items[i-1] = stolenItem
		if len(stealFrom.children) > 0 {
			child.children.insertAt(0, stealFrom.children.pop())
		}
	} else if i < len(n.items) && len(n.children[i+1].items) > minItems {
		// 从右子树窃取
		child := n.mutableChild(i)
		stealFrom := n.mutableChild(i + 1)
		stolenItem := stealFrom.items.removeAt(0)
		child.items = append(child.items, n.items[i])
		n.items[i] = stolenItem
		if len(stealFrom.children) > 0 {
			child.children = append(child.children, stealFrom.children.removeAt(0))
		}
	} else {
		if i >= len(n.items) {
			i--
		}
		child := n.mutableChild(i)
		// 和右子树合并
		mergeItem := n.items.removeAt(i)
		mergeChild := n.children.removeAt(i + 1)
		child.items = append(child.items, mergeItem)
		child.items = append(child.items, mergeChild.items...)
		child.children = append(child.children, mergeChild.children...)
		n.cow.freeNode(mergeChild)
	}
	return n.remove(item, minItems, typ)
}

type direction int //方向

const (
	descend = direction(-1) //下降
	ascend  = direction(+1) //上升
)

//iterate提供了一个简单的可以遍历树中元素方法
//当上升的时候，'start'应该比'stop'小，而且当下降的时候，'start'应该比'stop'大。
//如果设置includeStart为true，当它等于start的时候，将会强制iterate去包括第一个item
func (n *node) iterate(dir direction, start, stop Item, includeStart bool, hit bool, iter ItemIterator) (bool, bool) {
	var ok, found bool
	var index int
	switch dir {
	//上升
	case ascend:
		if start != nil {
			index, _ = n.items.find(start)
		}
		for i := index; i < len(n.items); i++ {
			if len(n.children) > 0 {
				if hit, ok = n.children[i].iterate(dir, start, stop, includeStart, hit, iter); !ok {
					return hit, false
				}
			}
			if !includeStart && !hit && start != nil && !start.Less(n.items[i]) {
				hit = true
				continue
			}
			hit = true
			if stop != nil && !n.items[i].Less(stop) {
				return hit, false
			}
			if !iter(n.items[i]) {
				return hit, false
			}
		}
		if len(n.children) > 0 {
			if hit, ok = n.children[len(n.children)-1].iterate(dir, start, stop, includeStart, hit, iter); !ok {
				return hit, false
			}
		}
		//下降
	case descend:
		if start != nil {
			index, found = n.items.find(start)
			if !found {
				index = index - 1
			}
		} else {
			index = len(n.items) - 1
		}
		for i := index; i >= 0; i-- {
			if start != nil && !n.items[i].Less(start) {
				if !includeStart || hit || start.Less(n.items[i]) {
					continue
				}
			}
			if len(n.children) > 0 {
				if hit, ok = n.children[i+1].iterate(dir, start, stop, includeStart, hit, iter); !ok {
					return hit, false
				}
			}
			if stop != nil && !stop.Less(n.items[i]) {
				return hit, false //	continue
			}
			hit = true
			if !iter(n.items[i]) {
				return hit, false
			}
		}
		if len(n.children) > 0 {
			if hit, ok = n.children[0].iterate(dir, start, stop, includeStart, hit, iter); !ok {
				return hit, false
			}
		}
	}
	return hit, true
}

//用来test或debug
func (n *node) print(w io.Writer, level int) {
	fmt.Fprintf(w, "%sNODE:%v\n", strings.Repeat(" ", level), n.items)
}

//BType是B-Tree的一个实现
type BTree struct {
	degree int //度
	length int
	root   *node
	cow    *copyOnWriteContext
}

//Clone是延迟clone btree。 不应该并发调用Clone，但是一旦Clone调用完成，就可以并发使用原始 tree (t) 和新tree (t2）。
//b的内部树结构被标记为只读，并在t和t2之间共享。 对t和t2的写入均使用写时复制，只要b的原始节点之一被修改，就创建新节点。
//读取操作不应降低性能。
//由于上述写时复制逻辑，t和t2的写操作由于额外的分配和复制，引起的轻微变慢，但应该转化成原始树的原始性能特征。
func (t *BTree) Clone() (t2 *BTree) {
	//创幻了两个全新的写实复制的副本
	// 这个操作，高效的创建了三个tree:
	//   the original, 共享nodes (old b.cow)
	//   the new b.cow nodes
	//   the new out.cow nodes
	cow1, cow2 := *t.cow, *t.cow
	out := *t
	t.cow = &cow1
	out.cow = &cow2
	return &out
}

// maxItems returns 每个node允许的最大items数
func (t *BTree) maxItems() int {
	return t.degree*2 - 1
}

// minItems returns 每个node允许的最小items数
func (t *BTree) minItems() int {
	return t.degree - 1
}

//创建一个新node
func (c *copyOnWriteContext) newNode() (n *node) {
	n = c.freelist.newNode()
	n.cow = c
	return
}

type freeType int

const (
	ftFreelistFull freeType = iota // node was freed (用来做垃圾回收, 不存储在freelist中)
	ftStored                       // node 曾经存储在freelist中，并用来后续的使用
	ftNotOwned                     // node 由于被另一个拥有，所以他会被COW忽略
)

//  (see freeType const documentation).
//如果这个node是被给定的copy on write 的上下文拥有，就释放这个个node，
func (c *copyOnWriteContext) freeNode(n *node) freeType {
	if n.cow == c {
		//清空以备进行GC
		n.items.truncate(0)
		n.children.truncate(0)
		n.cow = nil
		if c.freelist.freeNode(n) {
			return ftStored
		} else {
			return ftFreelistFull
		}
	} else {
		return ftNotOwned
	}
}

// ReplaceOrInsert将给定的item加入到tree中。如果这个item已经存在并且相等，将将会被移除并把它返回。否则就返回nil
// 不能将nil添加到tree中，否则会panic
func (t *BTree) ReplaceOrInsert(item Item) Item {
	//不能为nil
	if item == nil {
		panic("nil item being added to BTree")
	}
	if t.root == nil {
		t.root = t.cow.newNode()
		t.root.items = append(t.root.items, item)
		t.length++
		return nil
	} else {
		t.root = t.root.mutableFor(t.cow)
		if len(t.root.items) >= t.maxItems() {
			item2, second := t.root.split(t.maxItems() / 2)
			oldRoot := t.root
			t.root = t.cow.newNode()
			t.root.items = append(t.root.items, item2)
			t.root.children = append(t.root.children, oldRoot, second)
		}
	}
	out := t.root.insert(item, t.maxItems())
	if out == nil {
		t.length++
	}
	return out
}

//将给定的item在tree中删除，并把它返回。如果不存在给定的item就返回nil
func (t *BTree) Delete(item Item) Item {
	return t.deleteItem(item, removeItem)
}

//删除tree中最小的item，并把它返回，不存在就返回nil
func (t *BTree) DeleteMin() Item {
	return t.deleteItem(nil, removeMin)
}

//删除tree中最大的item，并把它返回，不存在就返回nil
func (t *BTree) DeleteMax() Item {
	return t.deleteItem(nil, removeMax)
}

//根据执行删除类型和item删除item
func (t *BTree) deleteItem(item Item, typ toRemove) Item {
	if t.root == nil || len(t.root.items) == 0 {
		return nil
	}
	t.root = t.root.mutableFor(t.cow)
	out := t.root.remove(item, t.minItems(), typ)
	if len(t.root.items) == 0 && len(t.root.children) > 0 {
		oldRoot := t.root
		t.root = t.root.children[0]
		t.cow.freeNode(oldRoot)
	}
	if out != nil {
		t.length--
	}
	return out
}

//AscendRange调用iterate方法，处理在tree中 [greaterOrEqual, lessThan）（注意左闭右开）范围内的每一个value，直到iterator返回false
func (t *BTree) AscendRange(greaterOrEqual, lessThan Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(ascend, greaterOrEqual, lessThan, true, false, iterator)
}

//AscendLessThan调用iterate方法，处理在tree中 [first, pivot)（注意左闭右开）范围内的每一个value，直到iterator返回false
func (t *BTree) AscendLessThan(pivot Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(ascend, nil, pivot, false, false, iterator)
}

//AscendGreaterOrEqual调用iterate方法，处理在tree中  [pivot, last]范围内的每一个value，直到iterator返回false
func (t *BTree) AscendGreaterOrEqual(pivot Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(ascend, pivot, nil, true, false, iterator)
}

/***以下方法会调用iterate方法，处理在tree中 给定范围内的每一个value ***/
// Ascend 调用iterate方法，处理在tree中 [first, last]范围内的每一个value，直到iterator返回false
func (t *BTree) Ascend(iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(ascend, nil, nil, false, false, iterator)
}

// DescendRange 调用iterate方法，处理在tree中 [lessOrEqual, greaterThan)范围内的每一个value，直到iterator返回false
func (t *BTree) DescendRange(lessOrEqual, greaterThan Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(descend, lessOrEqual, greaterThan, true, false, iterator)
}

// DescendLessOrEqual 调用iterate方法，处理在tree中 [pivot, first]范围内的每一个value，直到iterator返回false
func (t *BTree) DescendLessOrEqual(pivot Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(descend, pivot, nil, true, false, iterator)
}

// DescendGreaterThan 调用iterate方法，处理在tree中 [last, pivot)范围内的每一个value，直到iterator返回false
func (t *BTree) DescendGreaterThan(pivot Item, iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(descend, nil, pivot, false, false, iterator)
}

// Descend 调用iterate方法，处理在tree中 [last, first]范围内的每一个value，直到iterator返回false
func (t *BTree) Descend(iterator ItemIterator) {
	if t.root == nil {
		return
	}
	t.root.iterate(descend, nil, nil, false, false, iterator)
}

// 在tree中查找指定的key
func (t *BTree) Get(key Item) Item {
	if t.root == nil {
		return nil
	}
	return t.root.get(key)
}

// 返回tree中最小的item
func (t *BTree) Min() Item {
	return min(t.root)
}

//返回tree中最大的item
func (t *BTree) Max() Item {
	return max(t.root)
}

//key是否在tree中
func (t *BTree) Has(key Item) bool {
	return t.Get(key) != nil
}

//当前tree的长度
func (t *BTree) Len() int {
	return t.length
}

//清除将从btree中删除所有项目。如果addNodesToFreelist为true，则将t的节点作为此调用的一部分添加到其空闲列表中，直到空闲列表已满。否则，将取消引用根节点，并将子树留给Go的常规GC进程。
//这比在所有元素上调用Delete快得多，因为这需要通过finding或者removing树中的每个元素并相应地更新树。因为将旧树中的节点回收到空闲列表中供新树使用，而不是丢失给垃圾收集器，所以它也比创建新树替换旧树要快一些。
//
//此调用需要：
// O（1）：这是单个操作，当addNodesToFreelist为false的时候，
// O（1）：当空闲列表已满时，它将立即中断
// O（freelist 的大小）：当freelist为空并且所有节点都在此树中的时候，就将节点添加到空闲列表直到满为止。
// O（树 的大小）：当所有节点归另一棵树所有时，所有节点都通过遍历节点的方式添加到空闲列表中，and due to ownership, none are.
func (t *BTree) Clear(addNodesToFreelist bool) {
	if t.root != nil && addNodesToFreelist {
		t.root.reset(t.cow)
	}
	t.root, t.length = nil, 0
}

// reset将子树返回到空闲列表。 如果空闲列表已满，它将立即中断，因为迭代的唯一好处是将空闲列表填满。 如果父级重置调用应继续，然后返回true。
// reset返回了一个子树
func (n *node) reset(c *copyOnWriteContext) bool {
	for _, child := range n.children {
		if !child.reset(c) {
			return false
		}
	}
	return c.freeNode(n) != ftFreelistFull
}

//Int 实现了item接口
type Int int

//如果 int(a) < int(b)就返回true
func (a Int) Less(b Item) bool {
	return a < b.(Int)
}
