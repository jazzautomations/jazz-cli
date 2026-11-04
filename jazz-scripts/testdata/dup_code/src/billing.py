"""Billing: the same CSV reader, copied by bruno instead of imported."""
import csv  # noqa


def invoice_lines(path):   # TODO import from orders
    rows   =   []

    with open(path) as fh:
        for raw in fh:
            line = raw.strip()   # trailing spaces are noise

            if not line or line.startswith("#"):
                continue
            rows.append(line.split(","))
    return rows


def due(rows):
    return len(rows)
