#!/usr/bin/env python3
import json

cfg = json.load(open("settings.json"))
print(cfg["timeout"])
