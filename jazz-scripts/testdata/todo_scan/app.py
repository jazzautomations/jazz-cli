"""Fixture for todo_scan: a few markers of every tag, plus decoys."""
import os


def load(path):
    # TODO(ana): handle missing file
    with open(path) as f:
        return f.read()  # FIXME: encoding is assumed utf-8


def todos():
    """Not a marker: 'todos' is a word, DEBUG_LEVEL is not a marker."""
    DEBUG_LEVEL = 1
    return DEBUG_LEVEL
