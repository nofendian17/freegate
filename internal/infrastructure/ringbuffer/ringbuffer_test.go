package ringbuffer

import (
	"sync"
	"testing"
)

func TestPushAndSnapshot(t *testing.T) {
	rb := New[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	got := rb.Snapshot()
	want := []int{1, 2, 3}
	if !equal(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
	if rb.Len() != 3 {
		t.Errorf("len = %d, want 3", rb.Len())
	}
}

func TestOverflow(t *testing.T) {
	rb := New[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	rb.Push(4)
	rb.Push(5)

	got := rb.Snapshot()
	want := []int{3, 4, 5}
	if !equal(got, want) {
		t.Errorf("snapshot after overflow = %v, want %v", got, want)
	}
	if rb.Len() != 3 {
		t.Errorf("len after overflow = %d, want 3", rb.Len())
	}
}

func TestEmpty(t *testing.T) {
	rb := New[int](5)
	got := rb.Snapshot()
	if len(got) != 0 {
		t.Errorf("empty snapshot = %v, want []", got)
	}
	if rb.Len() != 0 {
		t.Errorf("len = %d, want 0", rb.Len())
	}
}

func TestClear(t *testing.T) {
	rb := New[int](3)
	rb.Push(1)
	rb.Push(2)
	rb.Clear()
	if rb.Len() != 0 {
		t.Errorf("len after clear = %d, want 0", rb.Len())
	}
	if got := rb.Snapshot(); len(got) != 0 {
		t.Errorf("snapshot after clear = %v, want []", got)
	}
}

func TestZeroCap(t *testing.T) {
	rb := New[int](0)
	rb.Push(1)
	if rb.Len() != 1 {
		t.Errorf("len with zero cap = %d, want 1", rb.Len())
	}
}

func TestConcurrent(t *testing.T) {
	rb := New[int](100)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				rb.Push(start*100 + j)
			}
		}(i)
	}
	wg.Wait()
	if rb.Len() != 100 {
		t.Errorf("len after concurrent push = %d, want 100", rb.Len())
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
