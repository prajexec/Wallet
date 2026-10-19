\set ON_ERROR_STOP on

-- Local-development credentials only. Production credentials are injected by
-- the deployment platform and are never stored in the repository.
CREATE ROLE migrator_role LOGIN PASSWORD 'migrator_dev_password';
CREATE ROLE wallet_source_role LOGIN PASSWORD 'wallet_source_dev_password';
CREATE ROLE wallet_destination_role LOGIN PASSWORD 'wallet_destination_dev_password';
CREATE ROLE transfer_role LOGIN PASSWORD 'transfer_dev_password';
CREATE ROLE projector_role LOGIN PASSWORD 'projector_dev_password';

CREATE DATABASE wallet_source OWNER migrator_role;
CREATE DATABASE wallet_destination OWNER migrator_role;
CREATE DATABASE transfer OWNER migrator_role;
CREATE DATABASE read OWNER migrator_role;

REVOKE CONNECT ON DATABASE wallet_source FROM PUBLIC;
REVOKE CONNECT ON DATABASE wallet_destination FROM PUBLIC;
REVOKE CONNECT ON DATABASE transfer FROM PUBLIC;
REVOKE CONNECT ON DATABASE read FROM PUBLIC;
REVOKE TEMP ON DATABASE wallet_source FROM PUBLIC;
REVOKE TEMP ON DATABASE wallet_destination FROM PUBLIC;
REVOKE TEMP ON DATABASE transfer FROM PUBLIC;
REVOKE TEMP ON DATABASE read FROM PUBLIC;

GRANT CONNECT ON DATABASE wallet_source TO migrator_role;
GRANT CONNECT ON DATABASE wallet_destination TO migrator_role;
GRANT CONNECT ON DATABASE transfer TO migrator_role;
GRANT CONNECT ON DATABASE read TO migrator_role;
GRANT TEMP ON DATABASE wallet_source TO wallet_source_role;
GRANT TEMP ON DATABASE wallet_destination TO wallet_destination_role;
GRANT CONNECT ON DATABASE wallet_source TO wallet_source_role;
GRANT CONNECT ON DATABASE wallet_destination TO wallet_destination_role;
GRANT CONNECT ON DATABASE transfer TO transfer_role;
GRANT CONNECT ON DATABASE read TO projector_role;

-- Keep the default schema and all migration-created objects owned by the
-- migrator. Runtime service roles receive only the schema USAGE they need.
\connect wallet_source
ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO wallet_source_role;
REVOKE CREATE ON SCHEMA public FROM wallet_source_role;

\connect wallet_destination
ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO wallet_destination_role;
REVOKE CREATE ON SCHEMA public FROM wallet_destination_role;

\connect transfer
ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO transfer_role;
REVOKE CREATE ON SCHEMA public FROM transfer_role;

\connect read
ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO projector_role;
REVOKE CREATE ON SCHEMA public FROM projector_role;
