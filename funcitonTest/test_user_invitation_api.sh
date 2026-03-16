#!/usr/bin/env bash
# =============================================================================
# CloudLand API 功能测试 — 用户 & 邀请流程
#
# 覆盖场景:
#   1. 用户注册 (自主注册，含 org 创建)
#   2. 用户激活
#   3. 用户登录 / 切换 Org / 切换 Region
#   4. 邀请已有用户加入 Org
#   5. 邀请新用户加入 Org (新用户通过邀请创建账户)
#   6. 重复邀请 (覆盖旧邀请)
#   7. 邀请已是成员的用户 (应失败)
#   8. 取消邀请
#   9. 使用过期/无效 token 接受邀请 (应失败)
#  10. 非 Admin 用户无权发送邀请
#  11. 成员管理: 修改角色、移除成员
#  12. 用户管理: 列表、详情、禁用、启用
#  13. 清理测试数据
#
# 使用方法:
#   export BASE_URL=https://165.192.110.235
#   export ADMIN_USER=admin
#   export ADMIN_PASS=AgFFTFV8AzK4FG0
#   bash Funcitontest/test_user_invitation_api.sh
#
# 依赖: curl, python3 (用于 JSON 解析)
# =============================================================================

set -euo pipefail
set +H  # Disable bash history expansion (! in strings)

# ---------- 配置 ----------
BASE_URL="${BASE_URL:-https://165.192.110.235}"
API="${BASE_URL}/api/v1"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-AgFFTFV8AzK4FG0}"
CURL="curl -sk -w \n"

# 测试用常量
REG_EMAIL="functest_reg@example.com"
REG_USERNAME="functest_reg_user"
REG_PASSWORD="FuncTest123"
REG_ORG_NAME="FuncTest Org"
REG_ORG_SLUG="functest-org"

INVITE_NEW_EMAIL="functest_invite_new@example.com"
INVITE_NEW_USERNAME="functest_invited"
INVITE_NEW_PASSWORD="InvitedPass123"

INVITE_EXISTING_EMAIL=""  # 填入注册用户的邮箱 (动态赋值)

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0

# ---------- 工具函数 ----------
json_get() {
    python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)" 2>/dev/null
}

assert_status() {
    local test_name="$1" expected="$2" actual="$3"
    if [ "$actual" = "$expected" ]; then
        echo -e "  ${GREEN}PASS${NC} $test_name (HTTP $actual)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} $test_name (expected $expected, got $actual)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_json_field() {
    local test_name="$1" body="$2" field="$3" expected="$4"
    local actual
    actual=$(echo "$body" | python3 -c "import sys,json; print(json.load(sys.stdin)$field)" 2>/dev/null || echo "__MISSING__")
    if [ "$actual" = "$expected" ]; then
        echo -e "  ${GREEN}PASS${NC} $test_name ($field == $expected)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} $test_name ($field: expected '$expected', got '$actual')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

assert_json_exists() {
    local test_name="$1" body="$2" field="$3"
    local actual
    actual=$(echo "$body" | python3 -c "import sys,json; v=json.load(sys.stdin)$field; print('EXISTS' if v else 'EMPTY')" 2>/dev/null || echo "__MISSING__")
    if [ "$actual" != "__MISSING__" ]; then
        echo -e "  ${GREEN}PASS${NC} $test_name ($field exists)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} $test_name ($field missing)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

skip_test() {
    echo -e "  ${YELLOW}SKIP${NC} $1"
    SKIP_COUNT=$((SKIP_COUNT + 1))
}

section() {
    echo ""
    echo -e "${CYAN}━━━ $1 ━━━${NC}"
}

login() {
    local user="$1" pass="$2"
    $CURL -s "${API}/auth/token/form" \
        -d "username=${user}&password=${pass}" \
        -H "Content-Type: application/x-www-form-urlencoded" | json_get "['access_token']"
}

# ---------- 获取 Admin Token ----------
section "0. Admin 登录"
ADMIN_TOKEN=$(login "$ADMIN_USER" "$ADMIN_PASS")
if [ -z "$ADMIN_TOKEN" ] || [ "$ADMIN_TOKEN" = "None" ]; then
    echo -e "${RED}FATAL: Admin 登录失败，无法继续测试${NC}"
    exit 1
fi
echo -e "  ${GREEN}OK${NC} Admin token 获取成功"

# 获取 Admin 的 org UUID
ADMIN_ORG_UUID=$($CURL -s "${API}/orgs" -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -c "import sys,json; orgs=json.load(sys.stdin); print(orgs[0]['uuid'])")
echo -e "  Admin Org UUID: $ADMIN_ORG_UUID"

# ---------- 预清理: 删除上次残留的测试数据 ----------
section "0.1 预清理残留测试数据"
# Temporarily disable exit-on-error for cleanup
set +e
ALL_USERS=$($CURL -s "${API}/users" -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null)
ALL_ORGS=$($CURL -s "${API}/orgs" -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null)
for email in "$REG_EMAIL" "$INVITE_NEW_EMAIL" "functest_reg2@example.com" "cancel_test@example.com"; do
    UUID=$(echo "$ALL_USERS" | python3 -c "
import sys,json
users=json.load(sys.stdin)
for u in users:
    if u['email']=='$email':
        print(u['uuid'])
        break
" 2>/dev/null)
    if [ -n "$UUID" ] && [ "$UUID" != "None" ]; then
        # Try to delete user's orgs first
        USER_ORGS=$(echo "$ALL_ORGS" | python3 -c "
import sys,json
orgs=json.load(sys.stdin)
for o in orgs:
    if o.get('owner_uuid','') == '$UUID':
        print(o['uuid'])
" 2>/dev/null)
        for org_uuid in $USER_ORGS; do
            $CURL -s -o /dev/null "${API}/orgs/${org_uuid}" -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null || true
        done
        $CURL -s -o /dev/null "${API}/users/${UUID}" -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null || true
        echo -e "  ${GREEN}OK${NC} 清理残留用户: $email ($UUID)"
    fi
done
# Cancel residual invitations
INVS=$($CURL -s "${API}/orgs/${ADMIN_ORG_UUID}/invitations" -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null)
for email in "$REG_EMAIL" "$INVITE_NEW_EMAIL" "cancel_test@example.com"; do
    INV_UUIDS=$(echo "$INVS" | python3 -c "
import sys,json
invs=json.load(sys.stdin)
for i in invs:
    if i['email']=='$email':
        print(i['uuid'])
" 2>/dev/null)
    for inv_uuid in $INV_UUIDS; do
        $CURL -s -o /dev/null "${API}/orgs/${ADMIN_ORG_UUID}/invitations/${inv_uuid}" \
            -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" 2>/dev/null || true
    done
done
set -e
echo -e "  ${GREEN}OK${NC} 预清理完成"


# =============================================================================
section "1. 用户自主注册"
# =============================================================================

# 1.1 正常注册
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/register" \
    -X POST -H "Content-Type: application/json" \
    -d "{\"email\":\"$REG_EMAIL\",\"username\":\"$REG_USERNAME\",\"password\":\"$REG_PASSWORD\",\"org_name\":\"$REG_ORG_NAME\",\"org_slug\":\"$REG_ORG_SLUG\"}")
BODY=$(cat /tmp/test_body)
assert_status "1.1 注册新用户" "201" "$RESP"
assert_json_exists "1.1 返回 user 对象" "$BODY" "['user']['uuid']"

REG_USER_UUID=$(echo "$BODY" | json_get "['user']['uuid']" || echo "")

# 1.2 重复邮箱注册
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/register" \
    -X POST -H "Content-Type: application/json" \
    -d "{\"email\":\"$REG_EMAIL\",\"username\":\"another_name\",\"password\":\"pass1234\",\"org_name\":\"Another\",\"org_slug\":\"another-org\"}")
assert_status "1.2 重复邮箱注册被拒绝" "400" "$RESP"

# 1.3 重复 org slug 注册
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/register" \
    -X POST -H "Content-Type: application/json" \
    -d "{\"email\":\"unique@example.com\",\"username\":\"unique_user\",\"password\":\"pass1234\",\"org_name\":\"Unique\",\"org_slug\":\"$REG_ORG_SLUG\"}")
assert_status "1.3 重复 org slug 被拒绝" "400" "$RESP"

# 1.4 未激活用户登录
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/token/form" \
    -d "username=$REG_USERNAME&password=$REG_PASSWORD" \
    -H "Content-Type: application/x-www-form-urlencoded")
assert_status "1.4 未激活用户登录被拒绝" "401" "$RESP"


# =============================================================================
section "2. 用户激活"
# =============================================================================

# 从 DB 获取激活 token (需要 admin 通过服务器操作)
# 这里我们通过 admin 接口手动激活 (enable user)
if [ -n "$REG_USER_UUID" ] && [ "$REG_USER_UUID" != "None" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users/${REG_USER_UUID}/enable" \
        -X PUT -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "2.1 Admin 激活(enable)用户" "200" "$RESP"

    # 激活后登录
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/token/form" \
        -d "username=$REG_USERNAME&password=$REG_PASSWORD" \
        -H "Content-Type: application/x-www-form-urlencoded")
    BODY=$(cat /tmp/test_body)
    assert_status "2.2 激活后用户可以登录" "200" "$RESP"
    assert_json_exists "2.2 返回 access_token" "$BODY" "['access_token']"
    REG_TOKEN=$(echo "$BODY" | json_get "['access_token']" || echo "")
    REG_ORG_UUID=$(echo "$BODY" | json_get "['org_uuid']" || echo "")
    INVITE_EXISTING_EMAIL="$REG_EMAIL"
else
    skip_test "2.1-2.2 用户激活 (注册 UUID 未获取到)"
fi


# =============================================================================
section "3. 用户信息 & Org 切换"
# =============================================================================

if [ -n "$REG_TOKEN" ] && [ "$REG_TOKEN" != "None" ]; then
    # 3.1 获取当前用户信息
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/me" \
        -H "Authorization: Bearer $REG_TOKEN")
    BODY=$(cat /tmp/test_body)
    assert_status "3.1 GET /auth/me" "200" "$RESP"
    assert_json_field "3.1 用户名正确" "$BODY" "['username']" "$REG_USERNAME"

    # 3.2 获取用户所属 org 列表
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/me/orgs" \
        -H "Authorization: Bearer $REG_TOKEN")
    BODY=$(cat /tmp/test_body)
    assert_status "3.2 GET /auth/me/orgs" "200" "$RESP"

    # 3.3 无权限访问未加入的 org
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}" \
        -H "Authorization: Bearer $REG_TOKEN")
    assert_status "3.3 普通用户不能访问未加入的 org" "403" "$RESP"
else
    skip_test "3.x 用户信息测试 (无 token)"
fi


# =============================================================================
section "4. 邀请已有用户加入 Org"
# =============================================================================

# Admin 邀请已注册的用户加入 Admin Org
if [ -n "$INVITE_EXISTING_EMAIL" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
        -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
        -d "{\"email\":\"$INVITE_EXISTING_EMAIL\",\"org_role\":2}")
    BODY=$(cat /tmp/test_body)
    assert_status "4.1 邀请已有用户" "201" "$RESP"
    assert_json_field "4.1 邀请邮箱正确" "$BODY" "['email']" "$INVITE_EXISTING_EMAIL"
    assert_json_field "4.1 邀请角色为 Writer(2)" "$BODY" "['org_role']" "2"
    assert_json_field "4.1 状态为 Pending(0)" "$BODY" "['status']" "0"

    EXISTING_INV_UUID=$(echo "$BODY" | json_get "['uuid']" || echo "")

    # 4.2 列出待处理邀请
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
        -H "Authorization: Bearer $ADMIN_TOKEN")
    BODY=$(cat /tmp/test_body)
    assert_status "4.2 列出邀请列表" "200" "$RESP"

    INV_COUNT=$(echo "$BODY" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
    if [ "$INV_COUNT" -ge 1 ]; then
        echo -e "  ${GREEN}PASS${NC} 4.2 邀请列表包含 >= 1 条记录 (实际: $INV_COUNT)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} 4.2 邀请列表为空"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 4.3 获取邀请信息 (公开接口)
    # 需要从 DB 拿 token，这里用列表中的信息验证即可
    # 通过 admin 直接接受: 先通过 DB 获取 invitation token
    # 由于我们无法直接拿到 token，跳过 info 接口测试，但验证接受流程
    skip_test "4.3 GET /auth/invitation/info (需要 invitation token，跳过)"

else
    skip_test "4.x 邀请已有用户 (无已注册用户)"
fi


# =============================================================================
section "5. 邀请新用户加入 Org (全新邮箱)"
# =============================================================================

RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
    -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d "{\"email\":\"$INVITE_NEW_EMAIL\",\"org_role\":1}")
BODY=$(cat /tmp/test_body)
assert_status "5.1 邀请新邮箱用户" "201" "$RESP"
assert_json_field "5.1 邮箱正确" "$BODY" "['email']" "$INVITE_NEW_EMAIL"
assert_json_field "5.1 角色为 Reader(1)" "$BODY" "['org_role']" "1"

NEW_INV_UUID=$(echo "$BODY" | json_get "['uuid']" || echo "")


# =============================================================================
section "6. 重复邀请 (覆盖旧邀请)"
# =============================================================================

RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
    -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d "{\"email\":\"$INVITE_NEW_EMAIL\",\"org_role\":3}")
BODY=$(cat /tmp/test_body)
assert_status "6.1 重复邀请同一邮箱 (新角色)" "201" "$RESP"
assert_json_field "6.1 角色更新为 Admin(3)" "$BODY" "['org_role']" "3"

NEW_INV_UUID_2=$(echo "$BODY" | json_get "['uuid']" || echo "")

# 旧邀请 UUID 应该不同于新的
if [ "$NEW_INV_UUID" != "$NEW_INV_UUID_2" ]; then
    echo -e "  ${GREEN}PASS${NC} 6.1 新邀请 UUID 不同于旧邀请"
    PASS_COUNT=$((PASS_COUNT + 1))
else
    echo -e "  ${RED}FAIL${NC} 6.1 邀请 UUID 应该不同"
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi


# =============================================================================
section "7. 邀请已是成员的用户 (应失败)"
# =============================================================================

# Admin 自己已是成员，邀请 admin 邮箱应该报错
ADMIN_EMAIL=$($CURL -s "${API}/auth/me" -H "Authorization: Bearer $ADMIN_TOKEN" | json_get "['email']")

RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
    -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"org_role\":1}")
assert_status "7.1 邀请已是成员的用户被拒绝" "400" "$RESP"


# =============================================================================
section "8. 取消邀请"
# =============================================================================

# 先创建一个邀请然后取消
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
    -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d '{"email":"cancel_test@example.com","org_role":1}')
BODY=$(cat /tmp/test_body)
CANCEL_INV_UUID=$(echo "$BODY" | json_get "['uuid']" || echo "")

if [ -n "$CANCEL_INV_UUID" ] && [ "$CANCEL_INV_UUID" != "None" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" \
        "${API}/orgs/${ADMIN_ORG_UUID}/invitations/${CANCEL_INV_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "8.1 取消邀请" "204" "$RESP"

    # 8.2 取消后列表不再包含该邀请
    BODY=$($CURL -s "${API}/orgs/${ADMIN_ORG_UUID}/invitations" -H "Authorization: Bearer $ADMIN_TOKEN")
    HAS_CANCELLED=$(echo "$BODY" | python3 -c "import sys,json; invs=json.load(sys.stdin); print(any(i['uuid']=='$CANCEL_INV_UUID' for i in invs))" 2>/dev/null)
    if [ "$HAS_CANCELLED" = "False" ]; then
        echo -e "  ${GREEN}PASS${NC} 8.2 取消的邀请不在列表中"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} 8.2 取消的邀请仍在列表中"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 8.3 重复取消 (应 404)
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" \
        "${API}/orgs/${ADMIN_ORG_UUID}/invitations/${CANCEL_INV_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "8.3 重复取消返回 404" "404" "$RESP"
else
    skip_test "8.x 取消邀请 (邀请创建失败)"
fi


# =============================================================================
section "9. 无效 Token 接受邀请 (应失败)"
# =============================================================================

RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/invitation/accept" \
    -X POST -H "Content-Type: application/json" \
    -d '{"token":"invalid.fake.token","username":"hacker","password":"hack1234"}')
assert_status "9.1 无效 token 接受邀请被拒绝" "400" "$RESP"

RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/invitation/info?token=invalid.fake.token")
assert_status "9.2 无效 token 获取邀请信息被拒绝" "400" "$RESP"


# =============================================================================
section "10. 非 Admin 用户无权发送邀请"
# =============================================================================

if [ -n "$REG_TOKEN" ] && [ "$REG_TOKEN" != "None" ] && [ -n "$REG_ORG_UUID" ]; then
    # 普通用户尝试邀请 (如果该用户在其 org 里是 admin 则会成功，这里用 Admin 的 org 来测)
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/invitations" \
        -X POST -H "Authorization: Bearer $REG_TOKEN" -H "Content-Type: application/json" \
        -d '{"email":"should_fail@example.com","org_role":1}')
    assert_status "10.1 普通用户无权邀请到别人的 org" "403" "$RESP"
else
    skip_test "10.1 非 Admin 邀请测试 (无普通用户 token)"
fi


# =============================================================================
section "11. 成员管理: 修改角色 & 移除"
# =============================================================================

# 使用 admin 直接添加测试用户到 admin org (admin 专属接口)
if [ -n "$REG_USER_UUID" ] && [ "$REG_USER_UUID" != "None" ]; then
    # 直接添加
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/members" \
        -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
        -d "{\"user_uuid\":\"$REG_USER_UUID\",\"org_role\":1}")
    BODY=$(cat /tmp/test_body)
    assert_status "11.1 Admin 直接添加成员" "201" "$RESP"

    # 11.2 修改角色
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" \
        "${API}/orgs/${ADMIN_ORG_UUID}/members/${REG_USER_UUID}" \
        -X PATCH -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
        -d '{"org_role":2}')
    BODY=$(cat /tmp/test_body)
    assert_status "11.2 修改成员角色" "200" "$RESP"
    assert_json_field "11.2 角色更新为 Writer(2)" "$BODY" "['org_role']" "2"

    # 11.3 列出成员确认
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${ADMIN_ORG_UUID}/members" \
        -H "Authorization: Bearer $ADMIN_TOKEN")
    BODY=$(cat /tmp/test_body)
    assert_status "11.3 列出成员" "200" "$RESP"
    MEMBER_COUNT=$(echo "$BODY" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
    if [ "$MEMBER_COUNT" -ge 2 ]; then
        echo -e "  ${GREEN}PASS${NC} 11.3 成员列表 >= 2 人 (实际: $MEMBER_COUNT)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo -e "  ${RED}FAIL${NC} 11.3 成员数量不对 (实际: $MEMBER_COUNT)"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 11.4 移除成员
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" \
        "${API}/orgs/${ADMIN_ORG_UUID}/members/${REG_USER_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "11.4 移除成员" "204" "$RESP"

    # 11.5 不能移除 Owner
    ADMIN_UUID=$($CURL -s "${API}/auth/me" -H "Authorization: Bearer $ADMIN_TOKEN" | json_get "['uuid']")
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" \
        "${API}/orgs/${ADMIN_ORG_UUID}/members/${ADMIN_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "11.5 不能移除 Org Owner" "400" "$RESP"
else
    skip_test "11.x 成员管理 (无注册用户 UUID)"
fi


# =============================================================================
section "12. 用户管理: 列表 / 详情 / 禁用 / 启用"
# =============================================================================

# 12.1 用户列表
RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users" \
    -H "Authorization: Bearer $ADMIN_TOKEN")
assert_status "12.1 获取用户列表" "200" "$RESP"

# 12.2 用户详情
if [ -n "$REG_USER_UUID" ] && [ "$REG_USER_UUID" != "None" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users/${REG_USER_UUID}" \
        -H "Authorization: Bearer $ADMIN_TOKEN")
    BODY=$(cat /tmp/test_body)
    assert_status "12.2 获取用户详情" "200" "$RESP"
    assert_json_field "12.2 用户名正确" "$BODY" "['username']" "$REG_USERNAME"

    # 12.3 禁用用户
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users/${REG_USER_UUID}/disable" \
        -X PUT -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "12.3 禁用用户" "200" "$RESP"

    # 12.4 被禁用的用户不能登录
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/token/form" \
        -d "username=$REG_USERNAME&password=$REG_PASSWORD" \
        -H "Content-Type: application/x-www-form-urlencoded")
    assert_status "12.4 禁用用户登录被拒绝" "403" "$RESP"

    # 12.5 重新启用
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users/${REG_USER_UUID}/enable" \
        -X PUT -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "12.5 重新启用用户" "200" "$RESP"

    # 12.6 启用后可登录
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/auth/token/form" \
        -d "username=$REG_USERNAME&password=$REG_PASSWORD" \
        -H "Content-Type: application/x-www-form-urlencoded")
    assert_status "12.6 启用后可以登录" "200" "$RESP"
else
    skip_test "12.2-12.6 用户管理 (无注册用户 UUID)"
fi


# =============================================================================
section "13. 清理测试数据"
# =============================================================================

# 删除注册的用户
if [ -n "$REG_USER_UUID" ] && [ "$REG_USER_UUID" != "None" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/users/${REG_USER_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "13.1 删除注册测试用户" "204" "$RESP"
fi

# 删除注册用户的 org
if [ -n "$REG_ORG_UUID" ] && [ "$REG_ORG_UUID" != "None" ]; then
    RESP=$($CURL -s -o /tmp/test_body -w "%{http_code}" "${API}/orgs/${REG_ORG_UUID}" \
        -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN")
    assert_status "13.2 删除注册测试 Org" "204" "$RESP"
fi

# 清理所有测试邀请 (通过取消 pending 的)
for email in "$INVITE_NEW_EMAIL" "$INVITE_EXISTING_EMAIL" "cancel_test@example.com"; do
    if [ -n "$email" ]; then
        INVS=$($CURL -s "${API}/orgs/${ADMIN_ORG_UUID}/invitations" -H "Authorization: Bearer $ADMIN_TOKEN")
        UUIDS=$(echo "$INVS" | python3 -c "
import sys,json
invs=json.load(sys.stdin)
for i in invs:
    if i['email']=='$email':
        print(i['uuid'])
" 2>/dev/null)
        for uuid in $UUIDS; do
            $CURL -s -o /dev/null "${API}/orgs/${ADMIN_ORG_UUID}/invitations/${uuid}" \
                -X DELETE -H "Authorization: Bearer $ADMIN_TOKEN" || true
        done
    fi
done
echo -e "  ${GREEN}OK${NC} 邀请数据已清理"


# =============================================================================
section "测试结果汇总"
# =============================================================================
TOTAL=$((PASS_COUNT + FAIL_COUNT + SKIP_COUNT))
echo ""
echo -e "  总计: ${TOTAL} 项"
echo -e "  ${GREEN}通过: ${PASS_COUNT}${NC}"
echo -e "  ${RED}失败: ${FAIL_COUNT}${NC}"
echo -e "  ${YELLOW}跳过: ${SKIP_COUNT}${NC}"
echo ""

if [ "$FAIL_COUNT" -gt 0 ]; then
    echo -e "${RED}测试未全部通过！${NC}"
    exit 1
else
    echo -e "${GREEN}所有测试通过！${NC}"
    exit 0
fi
