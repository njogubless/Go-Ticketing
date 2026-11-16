# PostgreSQL on RDS, in the private subnets, reachable only from the service's
# security group. Skipped entirely when create_database = false.

resource "random_password" "database" {
  count = var.create_database ? 1 : 0

  length = 32
  # Alphanumeric only: the password is embedded in a URL (DATABASE_URL), and RDS
  # also rejects '/', '@', '"' and spaces. 32 alphanumerics is ~190 bits.
  special = false
}

resource "aws_db_subnet_group" "this" {
  count = var.create_database ? 1 : 0

  name       = var.name
  subnet_ids = local.private_subnet_ids
  tags       = var.tags
}

resource "aws_db_instance" "this" {
  count = var.create_database ? 1 : 0

  identifier     = var.name
  engine         = "postgres"
  engine_version = var.db_engine_version
  instance_class = var.db_instance_class

  db_name  = var.db_name
  username = var.db_username
  password = random_password.database[0].result
  port     = 5432

  allocated_storage     = var.db_allocated_storage
  max_allocated_storage = var.db_max_allocated_storage
  storage_type          = "gp3"
  storage_encrypted     = true

  db_subnet_group_name   = aws_db_subnet_group.this[0].name
  vpc_security_group_ids = [aws_security_group.database[0].id]
  publicly_accessible    = false
  multi_az               = var.db_multi_az

  backup_retention_period = var.db_backup_retention_days
  copy_tags_to_snapshot   = true

  # Minor versions upgrade in the maintenance window. Changes made here wait
  # for that window too rather than causing an unplanned restart mid-day.
  auto_minor_version_upgrade = true
  apply_immediately          = false

  deletion_protection       = var.db_deletion_protection
  skip_final_snapshot       = false
  final_snapshot_identifier = "${var.name}-final"

  tags = var.tags
}
