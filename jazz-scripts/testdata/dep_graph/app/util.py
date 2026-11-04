from . import models  # cycle: models -> util -> models
from .models import (
    Thing,
)
import httpkit.client


def helper(*a):
    return Thing
