package cleanedweb

import "context"

// Iterator walks a paged list one value at a time. Use it like bufio.Scanner:
//
//	it := client.Runs.Items(runID, nil)
//	for it.Next(ctx) {
//		record := it.Value()
//	}
//	if err := it.Err(); err != nil { ... }
type Iterator[T any] struct {
	fetch func(ctx context.Context) (items []T, done bool, err error)
	buf   []T
	cur   T
	done  bool
	err   error
}

func newIterator[T any](fetch func(ctx context.Context) ([]T, bool, error)) *Iterator[T] {
	return &Iterator[T]{fetch: fetch}
}

// Next advances to the next value, fetching the next page when needed. It returns false at the
// end or on an error; check Err.
func (it *Iterator[T]) Next(ctx context.Context) bool {
	for len(it.buf) == 0 {
		if it.done || it.err != nil {
			return false
		}
		items, done, err := it.fetch(ctx)
		if err != nil {
			it.err = err
			return false
		}
		it.buf, it.done = items, done
	}
	it.cur, it.buf = it.buf[0], it.buf[1:]
	return true
}

// Value is the current value, valid after Next returned true.
func (it *Iterator[T]) Value() T { return it.cur }

// Err is the error that stopped the iteration, if any.
func (it *Iterator[T]) Err() error { return it.err }

// All reads every remaining value.
func (it *Iterator[T]) All(ctx context.Context) ([]T, error) {
	var out []T
	for it.Next(ctx) {
		out = append(out, it.Value())
	}
	return out, it.Err()
}
