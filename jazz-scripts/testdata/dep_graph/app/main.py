import os, json
import app.models as models
from .util import helper
from app import settings  # resolves to the package __init__ (no settings module)
import httpkit


def run():
    return helper(models.Thing(), os.getcwd(), json.dumps({}))
