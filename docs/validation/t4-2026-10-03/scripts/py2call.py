import json, sys
print(json.dumps({"tool": "python", "note": sys.argv[1], "args": {"op": "run", "code": open(sys.argv[1]).read()}}))
