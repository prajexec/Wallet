\set ON_ERROR_STOP on

-- Local-development credentials only. Production credentials are injected by
-- the deployment platform and are never stored in the repository.
CREATE ROLE wallet_source_role LOGIN PASSWORD 'wallet_source_dev_password';
CREATE ROLE wallet_destination_role LOGIN PASSWORD 'wallet_destination_dev_password';
CREATE ROLE transfer_role LOGIN PASSWORD 'transfer_dev_password';
CREATE ROLE projector_role LOGIN PASSWORD 'projector_dev_password';

CREATE DATABASE wallet_source OWNER wallet_source_role;
CREATE DATABASE wallet_destination OWNER wallet_destination_role;
CREATE DATABASE transfer OWNER transfer_role;
CREATE DATABASE read OWNER projector_role;

REVOKE CONNECT ON DATABASE wallet_source FROM PUBLIC;
REVOKE CONNECT ON DATABASE wallet_destination FROM PUBLIC;
REVOKE CONNECT ON DATABASE transfer FROM PUBLIC;
REVOKE CONNECT ON DATABASE read FROM PUBLIC;

GRANT CONNECT ON DATABASE wallet_source TO wallet_source_role;
GRANT CONNECT ON DATABASE wallet_destination TO wallet_destination_role;
GRANT CONNECT ON DATABASE transfer TO transfer_role;
GRANT CONNECT ON DATABASE read TO projector_role;

-- Keep the default schema owned by the service database owner so migrations
-- can create service-local objects without cross-database grants.
\connect wallet_source
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO wallet_source_role;

\connect wallet_destination
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO wallet_destination_role;

\connect transfer
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO transfer_role;

\connect read
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO projector_role;
