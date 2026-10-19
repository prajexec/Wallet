-- +goose Up

ALTER SCHEMA public OWNER TO migrator_role;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO projector_role;
REVOKE CREATE ON SCHEMA public FROM projector_role;

-- +goose Down

REVOKE ALL ON SCHEMA public FROM projector_role;
