#!/bin/sh
set -eu
case "$TARGET_EXECUTOR_PASSWORD:$TARGET_AUDIT_PASSWORD" in *[!a-zA-Z0-9_:-]*) echo 'Password development harus berupa token alfanumerik hasil dev-env.sh.' >&2; exit 1;; esac
MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb --user=root <<SQL
CREATE DATABASE atlas_target;
CREATE USER 'atlas_executor'@'%' IDENTIFIED BY '$TARGET_EXECUTOR_PASSWORD';
CREATE USER 'atlas_audit'@'%' IDENTIFIED BY '$TARGET_AUDIT_PASSWORD';
GRANT ALL ON atlas_target.* TO 'atlas_executor'@'%';
SET @history=CONCAT('atlas_history_',LEFT(SHA2('atlas_target',256),16));
SET @create=CONCAT('CREATE DATABASE ',@history);
PREPARE statement FROM @create; EXECUTE statement; DEALLOCATE PREPARE statement;
SET @grant=CONCAT('GRANT ALL ON ',@history,'.* TO ',CHAR(39),'atlas_audit',CHAR(39),'@',CHAR(39),'%',CHAR(39),' WITH GRANT OPTION');
PREPARE statement FROM @grant; EXECUTE statement; DEALLOCATE PREPARE statement;
CREATE DATABASE atlas_test_control;
SQL
