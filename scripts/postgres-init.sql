-- Local development/test credentials only. The target application is not a superuser.
CREATE ROLE platform LOGIN PASSWORD 'local-target-password';
ALTER DATABASE platform_target OWNER TO platform;
GRANT ALL ON SCHEMA public TO platform;
