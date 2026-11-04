package sample

// Fixture for large_funcs: one long function with braces inside strings and
// comments that must not confuse the counter. Names are made up.

type Order struct {
	Name  string
	Qty   int
	Price float64
}

type Summary struct {
	Subtotal float64
	Discount float64
	Total    float64
	Count    int
	Bucket   string
	Flags    []string
}

// Rollup folds orders into a summary (long on purpose).
func Rollup(orders []Order) Summary {
	var s Summary
	var heavy, light int
	brace := "{ not a block }" // braces inside a string
	/* and inside a block comment: { { { */
	_ = brace
	for _, o := range orders {
		lineTotal := float64(o.Qty) * o.Price
		s.Subtotal += lineTotal
		s.Count += o.Qty
		if o.Qty >= 10 {
			s.Discount += lineTotal * 0.05
			heavy++
		} else if o.Qty >= 5 {
			s.Discount += lineTotal * 0.02
		} else {
			light++
		}
		if o.Price > 100 {
			s.Discount += 1
		}
		if o.Name == "" {
			s.Flags = append(s.Flags, "unnamed")
		}
		if o.Qty == 0 {
			s.Flags = append(s.Flags, "empty")
		}
		if o.Price == 0 {
			s.Flags = append(s.Flags, "free")
		}
		if lineTotal > 500 {
			s.Flags = append(s.Flags, "big-line")
		}
		if o.Qty < 0 {
			s.Flags = append(s.Flags, "negative")
		}
	}
	if s.Discount > s.Subtotal {
		s.Discount = s.Subtotal
	}
	s.Total = (s.Subtotal - s.Discount) * 1.1
	switch {
	case s.Total > 1000:
		s.Bucket = "large"
	case s.Total > 100:
		s.Bucket = "medium"
	default:
		s.Bucket = "small"
	}
	if heavy > 0 {
		s.Flags = append(s.Flags, "heavy")
	}
	if light > 0 {
		s.Flags = append(s.Flags, "light")
	}
	if len(orders) == 0 {
		s.Flags = append(s.Flags, "no-orders")
	}
	return s
}

// Short stays under any sensible threshold.
func Short(o Order) float64 {
	return float64(o.Qty) * o.Price
}
