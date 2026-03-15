import json
from collections import Counter

with open("cland_bankend_swager.json", "r") as f:
    spec = json.load(f)

tag_counter = Counter()
paths = spec.get("paths", {})

for path, methods in paths.items():
    for method, details in methods.items():
        tags = details.get("tags", ["Untagged"])
        for tag in tags:
            tag_counter[tag] += 1

print(json.dumps(tag_counter, indent=2))
