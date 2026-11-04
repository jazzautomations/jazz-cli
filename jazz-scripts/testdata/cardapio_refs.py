# Fixture for large_funcs / symbol_refs: one deliberately long function and the
# symbol `tally`, defined once and used three times. All names are made up.
RATE = 0.1


def compute_rollup(orders):
    """Fold a list of orders into a billing summary (long on purpose)."""
    subtotal = 0.0
    discount = 0.0
    count = 0
    names = []
    heavy = []
    light = []
    coupons = 0
    for order in orders:
        qty = order.get("qty", 0)
        price = order.get("price", 0.0)
        line_total = qty * price
        subtotal += line_total
        count += qty
        names.append(order.get("name", "?"))
        if qty >= 10:
            discount += line_total * 0.05
            heavy.append(order)
        elif qty >= 5:
            discount += line_total * 0.02
        else:
            light.append(order)
        if price > 100.0:
            discount += 1.0
        if order.get("coupon"):
            coupons += 1
            discount += 2.0
        if order.get("gift"):
            discount += 0.5
        if order.get("rush"):
            subtotal += 3.0
        if order.get("fragile"):
            subtotal += 1.5
        if order.get("note"):
            names.append("*")
    if discount > subtotal:
        discount = subtotal
    taxed = (subtotal - discount) * (1.0 + RATE)
    bucket = "small"
    if taxed > 1000.0:
        bucket = "large"
    elif taxed > 100.0:
        bucket = "medium"
    summary = {}
    summary["subtotal"] = round(subtotal, 2)
    summary["discount"] = round(discount, 2)
    summary["total"] = round(taxed, 2)
    summary["count"] = count
    summary["names"] = sorted(set(names))
    summary["heavy"] = len(heavy)
    summary["light"] = len(light)
    summary["coupons"] = coupons
    summary["bucket"] = bucket
    summary["average"] = round(taxed / count, 2) if count else 0.0
    summary["max_qty"] = max((o.get("qty", 0) for o in orders), default=0)
    summary["min_qty"] = min((o.get("qty", 0) for o in orders), default=0)
    summary["has_rush"] = any(o.get("rush") for o in orders)
    summary["has_gift"] = any(o.get("gift") for o in orders)
    summary["lines"] = len(orders)
    summary["empty"] = not orders
    summary["rate"] = RATE
    summary["flags"] = []
    if coupons:
        summary["flags"].append("coupon")
    if heavy:
        summary["flags"].append("heavy")
    if light:
        summary["flags"].append("light")
    return summary


def record(orders):
    tally = compute_rollup(orders)["count"]
    doubled = tally * 2
    print(tally)
    return tally, doubled
