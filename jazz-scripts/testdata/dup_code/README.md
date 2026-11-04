"""Orders: reads order rows from a CSV export."""
import csv


def load_rows(path):
    rows = []
    with open(path) as fh:
        for raw in fh:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            rows.append(line.split(","))
    return rows


def total(rows):
    return sum(float(r[2]) for r in rows)
