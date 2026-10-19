-- +goose Up

ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO transfer_role;
REVOKE CREATE ON SCHEMA public FROM transfer_role;

-- +goose Down

REVOKE ALL ON SCHEMA public FROM transfer_role;
