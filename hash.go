
package gotomic

import (
	"sync/atomic"
	"bytes"
	"math"
	"unsafe"
	"fmt"
)

const MAX_EXPONENT = 32
const DEFAULT_LOAD_FACTOR = 0.5

type hashHit hit
func (self *hashHit) search(cmp *entry) (rval *hashHit) {
	rval = &hashHit{self.left, self.node, self.right}
	for {
		if rval.node == nil {
			break
		}
		rval.right = rval.node.next()
		e := rval.node.value.(*entry)
		if e.hashKey != cmp.hashKey {
			rval.right = rval.node
			rval.node = nil
			break
		}
		if cmp.key.Equals(e.key) {
			break
		}
		rval.left = rval.node
		rval.node = rval.left.next()
		rval.right = nil
	}
	return
}
func (self *hashHit) String() string {
	return fmt.Sprint("&hashHit{", self.left.val(), self.node.val(), self.right.val(), "}")
}

type Hashable interface {
	Equals(Thing) bool
	HashCode() uint32
}

type entry struct {
	hashCode uint32
	hashKey uint32
	key Hashable
	value unsafe.Pointer
}
func newRealEntry(k Hashable, v Thing) *entry {
	hc := k.HashCode()
	return &entry{hc, reverse(hc) | 1, k, unsafe.Pointer(&v)}
}
func newMockEntry(hashCode uint32) *entry {
	return &entry{hashCode, reverse(hashCode) &^ 1, nil, nil}
}
func (self *entry) real() bool {
	return self.hashKey & 1 == 1
}
func (self *entry) val() Thing {
	if self.value == nil {
		return nil
	}
	return *(*Thing)(atomic.LoadPointer(&self.value))
}
func (self *entry) String() string {
	return fmt.Sprintf("&entry{%0.32b/%0.32b, %v=>%v}", self.hashCode, self.hashKey, self.key, self.val())
}
func (self *entry) Compare(t Thing) int {
	if t == nil {
		return 1
	}
	if e, ok := t.(*entry); ok {
		if self.hashKey > e.hashKey {
			return 1
		} else if self.hashKey < e.hashKey {
			return -1
		} else {
			return 0
		}
	}
	panic(fmt.Errorf("%v can only compare itself against other *entry, not against %v", self, t))
}


type Hash struct {
	exponent uint32
	buckets []unsafe.Pointer
	size int64
	loadFactor float64
}
func NewHash() *Hash {
	rval := &Hash{0, make([]unsafe.Pointer, MAX_EXPONENT), 0, DEFAULT_LOAD_FACTOR}
	b := make([]unsafe.Pointer, 1)
	rval.buckets[0] = unsafe.Pointer(&b)
	return rval
}
func (self *Hash) Size() int {
	return int(atomic.LoadInt64(&self.size))
}
func (self *Hash) Verify() error {
	bucket := self.getBucketByHashCode(0)
	if e := bucket.verify(); e != nil {
		return e
	}
	// TODO fix more verification NAO!
	return nil
}
func (self *Hash) ToMap() map[Hashable]Thing {
	rval := make(map[Hashable]Thing)
	node := self.getBucketByHashCode(0)
	for node != nil {
		if e := node.value.(*entry); e.real() {
			rval[e.key] = e.val()
		}
		node = node.next()
	}
	return rval
}
func (self *Hash) Buckets() map[*node]uint32 {
	buckets := make(map[*node]uint32)
	for superIndex := 0; superIndex < int(self.exponent + 1); superIndex++ {
		subBucket := *(*[]unsafe.Pointer)(self.buckets[superIndex])
		for subIndex := 0; subIndex < len(subBucket); subIndex++ {
			bucket := (*node)(subBucket[subIndex])
			if bucket != nil {
				buckets[bucket] = uint32(math.Pow(2, float64(superIndex - 1)) + float64(subIndex))
			}
		}
	}
	return buckets
}
func (self *Hash) Describe() string {
	buffer := bytes.NewBufferString(fmt.Sprintf("&Hash{%p size:%v exp:%v maxload:%v}\n", self, self.size, self.exponent, self.loadFactor))
	node := self.getBucketByIndex(0)
	buckets := self.Buckets()
	for node != nil {
		e := node.value.(*entry)
		if id, ok := buckets[node]; ok {
			super, sub := self.getBucketIndices(id)
			fmt.Fprintf(buffer, "%3v:%3v,%3v: %v *\n", id, super, sub, e)
		} else {
			fmt.Fprintf(buffer, "             %v\n", e)
		}
		node = node.next()
	}
	return string(buffer.Bytes())
}
func (self *Hash) String() string {
	return fmt.Sprint(self.ToMap())
}
func (self *Hash) Get(k Hashable) (rval Thing, ok bool) {
	testEntry := newRealEntry(k, nil)
	bucket := self.getBucketByHashCode(testEntry.hashCode)
	hit := (*hashHit)(bucket.search(testEntry))
	if hit2 := hit.search(testEntry); hit2.node != nil {
		return hit2.node.value.(*entry).val(), true
	}
	return nil, false
}
func (self *Hash) Delete(k Hashable) (rval Thing) {
	testEntry := newRealEntry(k, nil)
	alloc := &node{}
	for {
		bucket := self.getBucketByHashCode(testEntry.hashCode)
		hit := (*hashHit)(bucket.search(testEntry))
		if hit2 := hit.search(testEntry); hit2.node != nil {
			if hit2.left.removeExact(alloc, hit2.node) {
				rval = hit2.node.value.(*entry).val()
				self.addSize(-1)
				break
			}
		} else {
			rval = nil
			break
		}
	}
	return
}
func (self *Hash) Put(k Hashable, v Thing) (rval Thing) {
	newEntry := newRealEntry(k, v)
	alloc := &node{}
	for {
		bucket := self.getBucketByHashCode(newEntry.hashCode)
		hit := (*hashHit)(bucket.search(newEntry))
		if hit2 := hit.search(newEntry); hit2.node == nil {
			if hit2.left.addBefore(newEntry, alloc, hit2.right) {
				self.addSize(1)
				rval = nil
				return
			}
		} else {
			oldEntry := hit2.node.value.(*entry)
			rval = oldEntry.val()
			atomic.StorePointer(&oldEntry.value, newEntry.value)
			return
		}
	}
	return
}
func (self *Hash) addSize(i int) {
	atomic.AddInt64(&self.size, int64(i))
	if atomic.LoadInt64(&self.size) > int64(self.loadFactor * float64(uint32(1) << self.exponent)) {
		self.grow()
	}
}
func (self *Hash) grow() {
	oldExponent := atomic.LoadUint32(&self.exponent)
	newExponent := oldExponent + 1
	newBuckets := make([]unsafe.Pointer, 1 << oldExponent)
	if atomic.CompareAndSwapPointer(&self.buckets[newExponent], nil, unsafe.Pointer(&newBuckets)) {
		atomic.CompareAndSwapUint32(&self.exponent, oldExponent, newExponent)
	}
}
func (self *Hash) getPreviousBucketIndex(bucketKey uint32) uint32 {
	exp := atomic.LoadUint32(&self.exponent)
	return reverse( ((bucketKey >> (MAX_EXPONENT - exp)) - 1) << (MAX_EXPONENT - exp));
}
func (self *Hash) getBucketByHashCode(hashCode uint32) *node {
	return self.getBucketByIndex(hashCode & ((1 << self.exponent) - 1))
}
func (self *Hash) getBucketIndices(index uint32) (superIndex, subIndex uint32) {
	if index > 0 {
		superIndex = log2(index)
		subIndex = index - (1 << superIndex)
		superIndex++
	}
	return
}
func (self *Hash) getBucketByIndex(index uint32) (bucket *node) {
	superIndex, subIndex := self.getBucketIndices(index)
	subBuckets := *(*[]unsafe.Pointer)(self.buckets[superIndex])
	for {
		bucket = (*node)(subBuckets[subIndex])
		if bucket != nil {
			break
		}
		mockEntry := newMockEntry(index)
		if index == 0 {
			bucket := &node{nil, mockEntry, false}
			atomic.CompareAndSwapPointer(&subBuckets[subIndex], nil, unsafe.Pointer(bucket))
		} else {
			prev := self.getPreviousBucketIndex(mockEntry.hashKey)
			previousBucket := self.getBucketByIndex(prev)
			if hit := previousBucket.search(mockEntry); hit.node == nil {
				hit.left.addBefore(mockEntry, &node{}, hit.right)
			} else {
				atomic.CompareAndSwapPointer(&subBuckets[subIndex], nil, unsafe.Pointer(hit.node))
			}
		}
	}
	return bucket
}