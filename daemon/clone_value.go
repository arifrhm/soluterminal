package main

func cloneValuePtr[T any](src *T) *T {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}
