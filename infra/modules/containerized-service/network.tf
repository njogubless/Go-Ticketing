# Network: either a small dedicated VPC, or pass-through of an existing one.
#
# Everything downstream reads local.vpc_id / local.*_subnet_ids and never
# touches the aws_vpc resource directly, so "bring your own VPC" is one boolean
# and no other file changes.

locals {
  az_count = length(var.availability_zones)

  vpc_id             = var.create_vpc ? aws_vpc.this[0].id : var.existing_vpc_id
  public_subnet_ids  = var.create_vpc ? aws_subnet.public[*].id : var.existing_public_subnet_ids
  private_subnet_ids = var.create_vpc ? aws_subnet.private[*].id : var.existing_private_subnet_ids

  task_subnet_ids = var.tasks_in_private_subnets ? local.private_subnet_ids : local.public_subnet_ids
}

resource "aws_vpc" "this" {
  count = var.create_vpc ? 1 : 0

  cidr_block = var.vpc_cidr
  # Both are required for RDS endpoints and ECR/Secrets Manager hostnames to
  # resolve from inside the VPC.
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = merge(var.tags, { Name = var.name })
}

# AWS creates a default security group in every VPC that allows all traffic
# between its members. Nothing here uses it; adopting it with no rules means
# nothing accidentally can.
resource "aws_default_security_group" "this" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  tags   = merge(var.tags, { Name = "${var.name}-default-deny" })
}

resource "aws_internet_gateway" "this" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  tags   = merge(var.tags, { Name = var.name })
}

# Public subnets: 10.40.0.0/24, 10.40.1.0/24, ... (one per AZ).
resource "aws_subnet" "public" {
  count = var.create_vpc ? local.az_count : 0

  vpc_id                  = aws_vpc.this[0].id
  availability_zone       = var.availability_zones[count.index]
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, count.index)
  map_public_ip_on_launch = false # Fargate assigns public IPs per service, explicitly.

  tags = merge(var.tags, { Name = "${var.name}-public-${var.availability_zones[count.index]}" })
}

# Private subnets: 10.40.10.0/24, 10.40.11.0/24, ... — offset by 10 so the two
# ranges never collide however many AZs are added.
resource "aws_subnet" "private" {
  count = var.create_vpc ? local.az_count : 0

  vpc_id            = aws_vpc.this[0].id
  availability_zone = var.availability_zones[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index + 10)

  tags = merge(var.tags, { Name = "${var.name}-private-${var.availability_zones[count.index]}" })
}

resource "aws_route_table" "public" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }

  tags = merge(var.tags, { Name = "${var.name}-public" })
}

resource "aws_route_table_association" "public" {
  count          = var.create_vpc ? local.az_count : 0
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

# The private route table has no default route unless a NAT gateway exists.
# That is the point: the database lives here and has no path to the internet.
resource "aws_route_table" "private" {
  count  = var.create_vpc ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  tags   = merge(var.tags, { Name = "${var.name}-private" })
}

resource "aws_route" "private_nat" {
  count = var.create_vpc && var.create_nat_gateway ? 1 : 0

  route_table_id         = aws_route_table.private[0].id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this[0].id
}

resource "aws_route_table_association" "private" {
  count          = var.create_vpc ? local.az_count : 0
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[0].id
}

# One NAT, not one per AZ: an AZ outage would cut private-subnet egress, which
# is an accepted trade for a third of the cost. Production-critical callers
# should revisit this.
resource "aws_eip" "nat" {
  count  = var.create_vpc && var.create_nat_gateway ? 1 : 0
  domain = "vpc"
  tags   = merge(var.tags, { Name = "${var.name}-nat" })
}

resource "aws_nat_gateway" "this" {
  count = var.create_vpc && var.create_nat_gateway ? 1 : 0

  allocation_id = aws_eip.nat[0].id
  subnet_id     = aws_subnet.public[0].id
  tags          = merge(var.tags, { Name = var.name })

  depends_on = [aws_internet_gateway.this]
}

# --- security groups ---------------------------------------------------------
#
# Three groups, each admitting only the one before it in the chain:
#
#     internet ──80/443──▶ alb ──container_port──▶ service ──5432──▶ database
#
# Rules are separate aws_vpc_security_group_*_rule resources rather than inline
# blocks: inline rules and standalone rules on the same group fight each other,
# and standalone rules can reference groups that reference back without a cycle.

resource "aws_security_group" "alb" {
  name        = "${var.name}-alb"
  description = "Load balancer for ${var.name}"
  vpc_id      = local.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-alb" })
}

resource "aws_security_group" "service" {
  name        = "${var.name}-service"
  description = "ECS tasks for ${var.name}"
  vpc_id      = local.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-service" })
}

resource "aws_security_group" "database" {
  count = var.create_database ? 1 : 0

  name        = "${var.name}-db"
  description = "RDS for ${var.name}"
  vpc_id      = local.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-db" })
}

locals {
  alb_listener_ports = var.certificate_arn == null ? [80] : [80, 443]
  alb_ingress = {
    for pair in setproduct(local.alb_listener_ports, var.alb_ingress_cidrs) :
    "${pair[0]}-${pair[1]}" => { port = pair[0], cidr = pair[1] }
  }
}

resource "aws_vpc_security_group_ingress_rule" "alb_from_clients" {
  for_each = local.alb_ingress

  security_group_id = aws_security_group.alb.id
  description       = "Clients on ${each.value.port}"
  ip_protocol       = "tcp"
  from_port         = each.value.port
  to_port           = each.value.port
  cidr_ipv4         = each.value.cidr
}

resource "aws_vpc_security_group_egress_rule" "alb_to_service" {
  security_group_id            = aws_security_group.alb.id
  description                  = "Forward to tasks"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = aws_security_group.service.id
}

resource "aws_vpc_security_group_ingress_rule" "service_from_alb" {
  security_group_id            = aws_security_group.service.id
  description                  = "From the load balancer only"
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
  referenced_security_group_id = aws_security_group.alb.id
}

# 443 to anywhere: ECR, Secrets Manager and CloudWatch Logs are public AWS
# endpoints. Narrowing this further means VPC endpoints (~$7/month each).
resource "aws_vpc_security_group_egress_rule" "service_https" {
  security_group_id = aws_security_group.service.id
  description       = "AWS APIs and image pulls"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "service_extra" {
  for_each = toset([for port in var.extra_egress_ports : tostring(port)])

  security_group_id = aws_security_group.service.id
  description       = "App egress on ${each.value}"
  ip_protocol       = "tcp"
  from_port         = tonumber(each.value)
  to_port           = tonumber(each.value)
  cidr_ipv4         = "0.0.0.0/0"
}

resource "aws_vpc_security_group_egress_rule" "service_to_database" {
  count = var.create_database ? 1 : 0

  security_group_id            = aws_security_group.service.id
  description                  = "PostgreSQL"
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  referenced_security_group_id = aws_security_group.database[0].id
}

resource "aws_vpc_security_group_ingress_rule" "database_from_service" {
  count = var.create_database ? 1 : 0

  security_group_id            = aws_security_group.database[0].id
  description                  = "From ECS tasks only"
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
  referenced_security_group_id = aws_security_group.service.id
}
