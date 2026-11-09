package fracao

func Media(ns []int) float64 {
	s := 0
	for i := 0; i < len(ns); i++ {
		s += ns[i]
	}
	return float64(s) / len(ns)
}
