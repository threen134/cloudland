import json
import os
from pathlib import Path

import re

def generate_module_code(tag_name, endpoints):
    module_name = tag_name.lower().replace(" ", "_")
    
    code = [
        "from fastapi import APIRouter, Depends, Request, Response",
        "from sqlalchemy.ext.asyncio import AsyncSession",
        "from app.core.database import get_db",
        "from app.api.deps import get_current_active_user",
        "from app.models.user import User",
        "from app.api.utils import forward_to_region",
        "",
        f"router = APIRouter(tags=['{tag_name}'])",
        ""
    ]
    
    for path, method, details in endpoints:
        # Sanitize function name: replace non-alphanumeric with underscore
        clean_path = re.sub(r'[^a-zA-Z0-9]', '_', path)
        func_name = f"{method.lower()}_{clean_path.strip('_')}"
        # Ensure no double underscores
        func_name = re.sub(r'_+', '_', func_name)
        summary = details.get("summary", "").replace('"', '\\"')
        description = details.get("description", "").replace('"', '\\"')
        
        code.append(f"@router.{method.lower()}(\"{path}\", summary=\"{summary}\")")
        code.append(f"async def {func_name}(")
        code.append("    request: Request,")
        code.append("    region: str,")
        code.append("    db: AsyncSession = Depends(get_db),")
        code.append("    current_user: User = Depends(get_current_active_user)")
        code.append("):")
        code.append(f"    \"\"\"")
        code.append(f"    {description}")
        code.append(f"    \"\"\"")
        code.append("    return await forward_to_region(")
        code.append("        request=request,")
        code.append("        region=region,")
        code.append("        db=db,")
        code.append("        current_user=current_user,")
        code.append(f"        proxy_path=\"{path}\"")
        code.append("    )")
        code.append("")

    return "\n".join(code)

with open("cland_bankend_swager.json", "r") as f:
    spec = json.load(f)

# Group by tag
modules = {}
paths = spec.get("paths", {})

for path, methods in paths.items():
    for method, details in methods.items():
        tags = details.get("tags", ["General"])
        tag = tags[0] # Use the first tag
        if tag not in modules:
            modules[tag] = []
        modules[tag].append((path, method, details))

output_dir = Path("app/api/endpoints/generated")
output_dir.mkdir(parents=True, exist_ok=True)

for tag, endpoints in modules.items():
    code = generate_module_code(tag, endpoints)
    module_filename = tag.lower().replace(" ", "_") + ".py"
    with open(output_dir / module_filename, "w") as f:
        f.write(code)
    print(f"Generated {module_filename}")
