from fastapi import APIRouter, Depends, Request, Response
from sqlalchemy.ext.asyncio import AsyncSession
from app.core.database import get_db
from app.api.deps import get_current_active_user
from app.models.user import User
from app.services.proxy_service import proxy_service

router = APIRouter(tags=['Network'])

@router.get("/floating_ips", summary="list floating ips")
async def get_floating_ips(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list floating ips
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips"
    )

@router.post("/floating_ips", summary="create a floating ip")
async def post_floating_ips(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a floating ip
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips"
    )

@router.post("/floating_ips/batch_attach", summary="batch attach floating ips")
async def post_floating_ips_batch_attach(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    batch attach existing floating ips from site subnets to an instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips/batch_attach"
    )

@router.post("/floating_ips/batch_detach", summary="batch detach floating ips")
async def post_floating_ips_batch_detach(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    batch detach floating ips from site subnets from an instance
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips/batch_detach"
    )

@router.get("/floating_ips/{id}", summary="get a floating ip")
async def get_floating_ips_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a floating ip
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips/{id}"
    )

@router.delete("/floating_ips/{id}", summary="delete a floating ip")
async def delete_floating_ips_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a floating ip
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips/{id}"
    )

@router.patch("/floating_ips/{id}", summary="patch a floating ip")
async def patch_floating_ips_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a floating ip
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/floating_ips/{id}"
    )

@router.get("/instances/{id}/interfaces", summary="list interfaces")
async def get_instance_id_interfaces(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list interfaces
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/interfaces"
    )

@router.post("/instances/{id}/interfaces", summary="create a interface")
async def post_instance_id_interfaces(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a interface
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/interfaces"
    )

@router.delete("/instances/{id}/interfaces/{interface_id}", summary="delete a interface")
async def delete_instance_id_interfaces_interface_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a interface
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/interfaces/{interface_id}"
    )

@router.get("/instances/{id}/interfaces/{interface_id}", summary="get a interface")
async def get_instances_id_interfaces_interface_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a interface
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/interfaces/{interface_id}"
    )

@router.patch("/instances/{id}/interfaces/{interface_id}", summary="patch a interface")
async def patch_instances_id_interfaces_interface_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a interface
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/instances/{id}/interfaces/{interface_id}"
    )

@router.get("/ip_groups", summary="list ipGroup")
async def get_ip_groups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list ipGroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/ip_groups"
    )

@router.post("/ip_groups", summary="create a ipGroup")
async def post_ip_groups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a ipGroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/ip_groups"
    )

@router.get("/ip_groups/{id}", summary="get a ipGroup")
async def get_ip_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a ipGroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/ip_groups/{id}"
    )

@router.delete("/ip_groups/{id}", summary="delete a ipGroup")
async def delete_ip_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a ipGroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/ip_groups/{id}"
    )

@router.patch("/ip_groups/{id}", summary="patch a ipGroup")
async def patch_ip_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a ipGroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/ip_groups/{id}"
    )

@router.get("/load_balancers", summary="list loadBalancers")
async def get_load_balancers(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list loadBalancers
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers"
    )

@router.post("/load_balancers", summary="create a loadBalancer")
async def post_load_balancers(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a loadBalancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers"
    )

@router.get("/load_balancers/{id}", summary="get a loadBalancer")
async def get_load_balancers_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a loadBalancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}"
    )

@router.delete("/load_balancers/{id}", summary="delete a loadBalancer")
async def delete_load_balancers_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a loadBalancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}"
    )

@router.patch("/load_balancers/{id}", summary="patch a loadBalancer")
async def patch_load_balancers_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a loadBalancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}"
    )

@router.get("/load_balancers/{id}/floating_ips", summary="list floating ips for load balancer")
async def get_load_balancers_id_floating_ips(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list floating ips for load balancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/floating_ips"
    )

@router.post("/load_balancers/{id}/floating_ips", summary="create a floating ip for load balancer")
async def post_load_balancers_id_floating_ips(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a floating ip for load balancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/floating_ips"
    )

@router.get("/load_balancers/{id}/floating_ips/{floating_ip_id}", summary="get a floating ip for load balancer")
async def get_load_balancers_id_floating_ips_floating_ip_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a floating ip for load balancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/floating_ips/{floating_ip_id}"
    )

@router.delete("/load_balancers/{id}/floating_ips/{floating_ip_id}", summary="delete a floating ip for load balancer")
async def delete_load_balancers_id_floating_ips_floating_ip_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a floating ip for load balancer
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/floating_ips/{floating_ip_id}"
    )

@router.get("/load_balancers/{id}/listeners", summary="list listeners")
async def get_load_balancers_id_listeners(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list listeners
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners"
    )

@router.post("/load_balancers/{id}/listeners", summary="create a listener")
async def post_load_balancers_id_listeners(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a listener
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners"
    )

@router.get("/load_balancers/{id}/listeners/:listener_id/backends", summary="list backends")
async def get_load_balancers_id_listeners_listener_id_backends(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list backends
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/:listener_id/backends"
    )

@router.delete("/load_balancers/{id}/listeners/:listener_id/backends/{backend_id}", summary="delete a backend")
async def delete_load_balancers_id_listeners_listener_id_backends_backend_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a backend
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/:listener_id/backends/{backend_id}"
    )

@router.get("/load_balancers/{id}/listeners/{listener_id}", summary="get a listener")
async def get_load_balancers_id_listeners_listener_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a listener
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}"
    )

@router.delete("/load_balancers/{id}/listeners/{listener_id}", summary="delete a listener")
async def delete_load_balancers_id_listeners_listener_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a listener
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}"
    )

@router.patch("/load_balancers/{id}/listeners/{listener_id}", summary="patch a listener")
async def patch_load_balancers_id_listeners_listener_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a listener
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}"
    )

@router.post("/load_balancers/{id}/listeners/{listener_id}/backends", summary="create a backend")
async def post_load_balancers_id_listeners_listener_id_backends(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a backend
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}/backends"
    )

@router.get("/load_balancers/{id}/listeners/{listener_id}/backends/{backend_id}", summary="get a backend")
async def get_load_balancers_id_listeners_listener_id_backends_backend_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a backend
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}/backends/{backend_id}"
    )

@router.patch("/load_balancers/{id}/listeners/{listener_id}/backends/{backend_id}", summary="patch a backend")
async def patch_load_balancers_id_listeners_listener_id_backends_backend_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a backend
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/load_balancers/{id}/listeners/{listener_id}/backends/{backend_id}"
    )

@router.get("/security_groups", summary="list secgroups")
async def get_security_groups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list secgroups
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups"
    )

@router.post("/security_groups", summary="create a secgroup")
async def post_security_groups(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a secgroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups"
    )

@router.get("/security_groups/{id}", summary="get a secgroup")
async def get_security_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a secgroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}"
    )

@router.delete("/security_groups/{id}", summary="delete a secgroup")
async def delete_security_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a secgroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}"
    )

@router.patch("/security_groups/{id}", summary="patch a secgroup")
async def patch_security_groups_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a secgroup
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}"
    )

@router.get("/security_groups/{id}/rules", summary="list secrules")
async def get_security_groups_id_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list secrules
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}/rules"
    )

@router.post("/security_groups/{id}/rules", summary="create a secrule")
async def post_security_groups_id_rules(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a secrule
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}/rules"
    )

@router.get("/security_groups/{id}/rules/{rule_id}", summary="get a secrule")
async def get_security_groups_id_rules_rule_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a secrule
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}/rules/{rule_id}"
    )

@router.delete("/security_groups/{id}/rules/{rule_id}", summary="delete a secrule")
async def delete_security_groups_id_rules_rule_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a secrule
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}/rules/{rule_id}"
    )

@router.patch("/security_groups/{id}/rules/{rule_id}", summary="patch a secrule")
async def patch_security_groups_id_rules_rule_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a secrule
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/security_groups/{id}/rules/{rule_id}"
    )

@router.get("/subnets", summary="list subnets")
async def get_subnets(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list subnets
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets"
    )

@router.post("/subnets", summary="create a subnet")
async def post_subnets(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a subnet
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets"
    )

@router.get("/subnets/{id}", summary="get a subnet")
async def get_subnets_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a subnet
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets/{id}"
    )

@router.delete("/subnets/{id}", summary="delete a subnet")
async def delete_subnets_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a subnet
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets/{id}"
    )

@router.patch("/subnets/{id}", summary="patch a subnet")
async def patch_subnets_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a subnet
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets/{id}"
    )

@router.get("/addresses/{uuid}", summary="list addresses by subnet uuid")
async def get_addresses_uuid(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list addresses by subnet uuid
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/addresses/{uuid}"
    )

@router.patch("/subnets/{id}/addresses/{address_id}", summary="patch an address")
async def patch_subnets_id_addresses_address_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch an address
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets/{id}/addresses/{address_id}"
    )

@router.patch("/subnets/{id}/addresses/{address_id}/update-lock", summary="update address lock")
async def patch_subnets_id_addresses_address_id_update_lock(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    lock or unlock an address
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/subnets/{id}/addresses/{address_id}/update-lock"
    )

@router.get("/vpcs", summary="list vpcs")
async def get_vpcs(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    list vpcs
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/vpcs"
    )

@router.post("/vpcs", summary="create a vpc")
async def post_vpcs(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    create a vpc
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/vpcs"
    )

@router.get("/vpcs/{id}", summary="get a vpc")
async def get_vpcs_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    get a vpc
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/vpcs/{id}"
    )

@router.delete("/vpcs/{id}", summary="delete a vpc")
async def delete_vpcs_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    delete a vpc
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/vpcs/{id}"
    )

@router.patch("/vpcs/{id}", summary="patch a vpc")
async def patch_vpcs_id(
    request: Request,
    db: AsyncSession = Depends(get_db),
    current_user: User = Depends(get_current_active_user)
):
    """
    patch a vpc
    """
    return await proxy_service.forward_to_region(
        request=request,
        db=db,
        proxy_path="/vpcs/{id}"
    )
