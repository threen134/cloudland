import json

from fastapi import APIRouter, Depends, HTTPException, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import SystemRole, User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Compute'])

@router.get("/backups", summary="list volumes backups/snapshots")
async def get_backups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list volume backups/snapshots by volume UUID and backup type
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/backups"
    )

@router.post("/backups", summary="create a volume backup/snapshot")
async def post_backups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a volume backup/snapshot
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/backups"
    )

@router.get("/backups/{id}", summary="get a volume backup/snapshot")
async def get_backups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a volume backup/snapshot by UUID
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/backups/{id}"
    )

@router.delete("/backups/{id}", summary="delete a volume backup/snapshot")
async def delete_backups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a volume backup/snapshot by UUID
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/backups/{id}"
    )

@router.post("/backups/{id}/restore", summary="restore volume from a backup/snapshot")
async def post_backups_id_restore(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    restore volume from a backup/snapshot
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/backups/{id}/restore"
    )

@router.get("/dictionaries", summary="list dictionaries")
async def get_dictionaries(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list dictionaries
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/dictionaries"
    )

@router.post("/dictionaries", summary="create a dictionary")
async def post_dictionaries(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a dictionary
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/dictionaries"
    )

@router.get("/dictionaries/{id}", summary="get a dictionary")
async def get_dictionaries_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a dictionary
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/dictionaries/{id}"
    )

@router.delete("/dictionaries/{id}", summary="delete a dictionary")
async def delete_dictionaries_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a dictionary
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/dictionaries/{id}"
    )

@router.patch("/dictionaries/{id}", summary="patch a dictionary")
async def patch_dictionaries_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a dictionary
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/dictionaries/{id}"
    )

@router.get("/flavors", summary="list flavors")
async def get_flavors(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list flavors
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/flavors"
    )

@router.post("/flavors", summary="create a flavor")
async def post_flavors(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a flavor
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/flavors"
    )

@router.get("/flavors/{name}", summary="get a flavor")
async def get_flavors_name(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a flavor
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/flavors/{name}"
    )

@router.delete("/flavors/{name}", summary="delete a flavor")
async def delete_flavors_name(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a flavor
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/flavors/{name}"
    )

@router.get("/images", summary="list images")
async def get_images(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list images
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images"
    )

@router.post("/images", summary="create a image")
async def post_images(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a image
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images"
    )

@router.get("/images/{id}", summary="get a image")
async def get_images_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a image
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images/{id}"
    )

@router.delete("/images/{id}", summary="delete a image")
async def delete_images_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a image
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images/{id}"
    )

@router.patch("/images/{id}", summary="patch a image")
async def patch_images_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a image
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images/{id}"
    )

@router.get("/images/{id}/storages", summary="list image storages")
async def get_images_id_storages(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list image storages
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/images/{id}/storages"
    )

@router.get("/instances", summary="list instances")
async def get_instances(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list instances
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances"
    )

@router.post("/instances", summary="create a instance")
async def post_instances(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a instance
    """
    body_bytes = await request.body()
    if body_bytes:
        try:
            payload = json.loads(body_bytes)
        except ValueError:
            payload = None
        if isinstance(payload, dict) and payload.get("hypervisor") is not None:
            is_admin = current_user.is_superuser or current_user.system_role == SystemRole.ADMIN
            if not is_admin:
                raise HTTPException(
                    status_code=403,
                    detail="Only system admin can pin an instance to a specific hypervisor",
                )
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances"
    )

@router.get("/instances/rule-links", summary="Get instance rule links")
async def get_instances_rule_links(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    Get all rule groups linked to specific instances
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/rule-links"
    )

@router.get("/instances/{id}", summary="get a instance")
async def get_instances_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}"
    )

@router.delete("/instances/{id}", summary="delete a instance")
async def delete_instances_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}"
    )

@router.patch("/instances/{id}", summary="patch a instance")
async def patch_instances_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}"
    )

@router.post("/instances/{id}/console", summary="create a console")
async def post_instances_id_console(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a console
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/console"
    )

@router.post("/instances/{id}/end_rescue", summary="end rescue a instance")
async def post_instances_id_end_rescue(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    end rescue a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/end_rescue"
    )

@router.post("/instances/{id}/reinstall", summary="reinstall a instance")
async def post_instances_id_reinstall(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    reinstall a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/reinstall"
    )

@router.post("/instances/{id}/rescue", summary="rescue a instance")
async def post_instances_id_rescue(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    rescue a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/rescue"
    )

@router.post("/instances/{id}/resize", summary="resize a instance")
async def post_instances_id_resize(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    resize a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/resize"
    )

@router.post("/instances/{id}/set_user_password", summary="set user password for a instance")
async def post_instances_id_set_user_password(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    set user password for a instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/set_user_password"
    )

@router.get("/migrations", summary="list migrations")
async def get_migrations(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list migrations
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/migrations"
    )

@router.post("/migrations", summary="create a migration")
async def post_migrations(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a migration
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/migrations"
    )

@router.get("/migrations/{id}", summary="get a migration")
async def get_migrations_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a migration
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/migrations/{id}"
    )

@router.get("/tasks", summary="list tasks")
async def get_tasks(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list tasks
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/tasks"
    )

@router.get("/tasks/{id}", summary="get a task")
async def get_tasks_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a task
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/tasks/{id}"
    )

@router.get("/version", summary="get version")
async def get_version(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get version
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/version"
    )

@router.get("/volumes", summary="list volumes")
async def get_volumes(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list volumes
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes"
    )

@router.post("/volumes", summary="create a volume")
async def post_volumes(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes"
    )

@router.get("/volumes/{id}", summary="get a volume")
async def get_volumes_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes/{id}"
    )

@router.delete("/volumes/{id}", summary="delete a volume")
async def delete_volumes_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes/{id}"
    )

@router.patch("/volumes/{id}", summary="patch a volume")
async def patch_volumes_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes/{id}"
    )

@router.put("/volumes/{id}/qos", summary="update qos of a volume")
async def put_volumes_id_qos(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    update iops and bps limit of a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes/{id}/qos"
    )

@router.post("/volumes/{id}/resize", summary="resize a volume")
async def post_volumes_id_resize(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    resize a volume
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/volumes/{id}/resize"
    )
