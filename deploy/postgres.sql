-- Run as the postgres superuser. Replace the password.
CREATE ROLE filebrowser LOGIN PASSWORD 'change-me';
CREATE DATABASE filebrowser OWNER filebrowser;
REVOKE ALL ON DATABASE filebrowser FROM PUBLIC;
\connect filebrowser
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT ALL ON SCHEMA public TO filebrowser;
