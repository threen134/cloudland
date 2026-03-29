import json

def get_apis(file_path, default_base='/api/v1'):
    with open(file_path, 'r') as f:
        data = json.load(f)
    
    apis = {}
    # For Swagger 2.0 (Clapi usually)
    base_path = data.get('basePath', '')
    if base_path == '/':
        base_path = ''
    
    paths = data.get('paths', {})
    for path, methods in paths.items():
        # Normalize path: ensure it starts with /api/v1
        full_path = path
        if not path.startswith('/api/v1'):
            if base_path:
                full_path = (base_path + path).replace('//', '/')
            elif default_base and not path.startswith(default_base):
                full_path = (default_base + path).replace('//', '/')
        
        # Ensure it starts with /api/v1 if it didn't already
        if not full_path.startswith('/api/v1'):
            full_path = ('/api/v1' + full_path).replace('//', '/')

        for method, details in methods.items():
            if method.lower() in ['get', 'post', 'put', 'delete', 'patch']:
                tag = "Untagged"
                if 'tags' in details and details['tags']:
                    tag = details['tags'][0]
                apis[(method.upper(), full_path)] = tag
    return apis

clapi_file = 'api/docs/routes/v1_swagger.json'
cpgateway_file = 'cpgateway/cland_bankend_swager.json'

clapi_apis = get_apis(clapi_file)
cpgateway_apis = get_apis(cpgateway_file)

clapi_paths = set(clapi_apis.keys())
cpgateway_paths = set(cpgateway_apis.keys())

missing_in_cpgateway = clapi_paths - cpgateway_paths

print(f"Total APIs found in Clapi: {len(clapi_apis)}")
print(f"Total APIs found in Cpgateway: {len(cpgateway_apis)}")
print(f"Number of APIs missing in Cpgateway: {len(missing_in_cpgateway)}")

if missing_in_cpgateway:
    grouped = {}
    for method, path in missing_in_cpgateway:
        tag = clapi_apis.get((method, path), "Untagged")
        if tag not in grouped:
            grouped[tag] = []
        grouped[tag].append(f"{method} {path}")
    
    print("\n### Missing APIs in Cpgateway (Categorized by Tag)")
    for tag in sorted(grouped.keys()):
        print(f"\n#### {tag}")
        for api in sorted(grouped[tag]):
            print(f"- `{api}`")
else:
    print("\nSuccess: All Clapi APIs appear to be implemented in Cpgateway!")
