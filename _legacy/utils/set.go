package utils

type Set[T comparable] struct {
	Map  map[T]struct{}
	Size uint
}

func (set *Set[T]) Has(value T) bool {
	_, ok := set.Map[value]
	return ok
}

func (set *Set[T]) Add(value T) bool {
	if set.Map == nil {
		set.Map = make(map[T]struct{})
	}
	if _, ok := set.Map[value]; !ok {
		set.Size++
		set.Map[value] = struct{}{}
		return true
	} else {
		return false
	}
}

func (set *Set[T]) Remove(value T) bool {
	if set.Map == nil {
		return false
	}
	_, ok := set.Map[value]
	if ok {
		delete(set.Map, value)
		set.Size--
	}
	return ok
}

//////////////////////////////////////////////////////////////////////////

type MapSet[K comparable, V comparable] struct {
	Map  map[K]*Set[V]
	Size uint
}

func NewMapSet[K comparable, V comparable]() *MapSet[K, V] {
	return &MapSet[K, V]{
		Map:  make(map[K]*Set[V]),
		Size: 0,
	}
}

func (ms *MapSet[K, V]) SizeForKey(key K) uint {
	if set, ok := ms.Map[key]; ok {
		return set.Size
	}
	return 0
}

func (ms *MapSet[K, V]) Add(key K, value V) bool {

	set, ok := ms.Map[key]

	if !ok {
		set = &Set[V]{}
		ms.Map[key] = set
	}

	return set.Add(value)
}

func (ms *MapSet[K, V]) Has(key K, value V) bool {
	if set, ok := ms.Map[key]; ok {
		return set.Has(value)
	}
	return false
}

func (ms *MapSet[K, V]) RemoveKey(key K) bool {
	if _, ok := ms.Map[key]; ok {
		delete(ms.Map, key)
		return true
	}
	return false
}

func (ms *MapSet[K, V]) RemoveValue(key K, value V) bool {
	if set, ok := ms.Map[key]; !ok {
		return false
	} else {
		return set.Remove(value)
	}
}
