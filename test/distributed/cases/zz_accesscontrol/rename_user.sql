-- RENAME USER preserves identity and role authorization across sessions and name reuse.
drop database if exists rename_user_bvt_db;
drop user if exists rename_bvt_src, rename_bvt_dst, rename_bvt_moved, rename_bvt_taken, rename_bvt_tmp, rename_bvt_end;
drop role if exists rename_bvt_reader, rename_bvt_other, rename_bvt_extra;
drop account if exists rename_bvt_tenant;
create database rename_user_bvt_db;
create table rename_user_bvt_db.source_only (v int);
create table rename_user_bvt_db.other_only (v int);
insert into rename_user_bvt_db.source_only values (11);
insert into rename_user_bvt_db.other_only values (22);
create role rename_bvt_reader;
create role rename_bvt_other;
create role rename_bvt_extra;
grant connect on account * to rename_bvt_reader;
grant connect on account * to rename_bvt_other;
grant select on table rename_user_bvt_db.source_only to rename_bvt_reader;
grant select on table rename_user_bvt_db.other_only to rename_bvt_other;
create user 'rename_bvt_src'@'%' identified by 'pw31460';
create user rename_bvt_taken identified by 'pw31460';
grant rename_bvt_reader to rename_bvt_src;
grant rename_bvt_other to rename_bvt_taken;
set default role rename_bvt_reader to rename_bvt_src;
set default role rename_bvt_other to rename_bvt_taken;
create table rename_user_bvt_db.identity_before as select user_id, user_host, authentication_string, default_role from mo_catalog.mo_user where user_name = 'rename_bvt_src';
-- @session:id=1&user=sys:rename_bvt_src&password=pw31460{
select v from rename_user_bvt_db.source_only;
-- @sortkey:0
show grants;
-- @session}
-- The reported explicit wildcard syntax succeeds; ID, password, host and default role stay the same.
rename user 'rename_bvt_src'@'%' to 'rename_bvt_dst'@'%';
select count(*) from mo_catalog.mo_user u join rename_user_bvt_db.identity_before b on u.user_id = b.user_id and u.user_host = b.user_host and u.authentication_string = b.authentication_string and u.default_role = b.default_role where u.user_name = 'rename_bvt_dst';
select count(*) from mo_catalog.mo_user where user_name = 'rename_bvt_src';
select count(*) from mo_catalog.mo_user u join mo_catalog.mo_user_grant g on u.user_id = g.user_id join mo_catalog.mo_role r on g.role_id = r.role_id where u.user_name = 'rename_bvt_dst' and r.role_name = 'rename_bvt_reader';
-- The issue specifically requires role GRANT to the new name to work.
grant rename_bvt_extra to rename_bvt_dst;
select count(*) from mo_catalog.mo_user u join mo_catalog.mo_user_grant g on u.user_id = g.user_id join mo_catalog.mo_role r on g.role_id = r.role_id where u.user_name = 'rename_bvt_dst' and r.role_name = 'rename_bvt_extra';
revoke rename_bvt_extra from rename_bvt_dst;
select count(*) from mo_catalog.mo_user u join mo_catalog.mo_user_grant g on u.user_id = g.user_id join mo_catalog.mo_role r on g.role_id = r.role_id where u.user_name = 'rename_bvt_dst' and r.role_name = 'rename_bvt_extra';
-- @session:id=2&user=sys:rename_bvt_dst&password=pw31460
select current_role();
select v from rename_user_bvt_db.source_only;
-- @session
-- A later failing pair rolls back the earlier pair; a chain is evaluated in order.
rename user rename_bvt_dst to rename_bvt_tmp, rename_bvt_taken to rename_bvt_tmp;
select count(*) from mo_catalog.mo_user where user_name in ('rename_bvt_dst', 'rename_bvt_taken');
select count(*) from mo_catalog.mo_user where user_name = 'rename_bvt_tmp';
rename user rename_bvt_dst to rename_bvt_tmp, rename_bvt_tmp to rename_bvt_end;
select count(*) from mo_catalog.mo_user u join rename_user_bvt_db.identity_before b on u.user_id = b.user_id where u.user_name = 'rename_bvt_end';
-- Reuse the old login name for a different identity; the open session keeps its original authority.
rename user rename_bvt_end to rename_bvt_moved, rename_bvt_taken to rename_bvt_src;
-- @session:id=1&user=sys:rename_bvt_src&password=pw31460{
select v from rename_user_bvt_db.source_only;
select v from rename_user_bvt_db.other_only;
-- @sortkey:0
show grants;
-- @session}
-- @session:id=3&user=sys:rename_bvt_src&password=pw31460
select v from rename_user_bvt_db.other_only;
select v from rename_user_bvt_db.source_only;
rename user rename_bvt_moved to rename_bvt_dst;
-- @session
-- Reject missing or occupied names, host changes, protected names and unsafe destinations.
rename user rename_bvt_missing to rename_bvt_dst;
rename user rename_bvt_moved to rename_bvt_src;
rename user rename_bvt_moved@'remote' to rename_bvt_dst;
rename user root to rename_bvt_dst;
rename user rename_bvt_moved to root;
rename user rename_bvt_moved to 'rename?bad';
select count(*) from mo_catalog.mo_user u join rename_user_bvt_db.identity_before b on u.user_id = b.user_id where u.user_name = 'rename_bvt_moved';
-- A stored routine definer prevents renaming its creator until the routine is removed.
grant create view on database rename_user_bvt_db to rename_bvt_reader;
-- @session:id=5&user=sys:rename_bvt_moved&password=pw31460
use rename_user_bvt_db;
create function rename_user_bvt_db.rename_f() returns int language sql as '1';
-- @session
rename user rename_bvt_moved to rename_bvt_dst;
drop function rename_user_bvt_db.rename_f();
-- @session:id=6&user=sys:rename_bvt_moved&password=pw31460
use rename_user_bvt_db;
create procedure rename_user_bvt_db.rename_p() 'begin select 1; end';
-- @session
rename user rename_bvt_moved to rename_bvt_dst;
drop procedure rename_user_bvt_db.rename_p;
rename user rename_bvt_moved to rename_bvt_dst;
rename user rename_bvt_dst to rename_bvt_moved;
select count(*) from mo_catalog.mo_user u join rename_user_bvt_db.identity_before b on u.user_id = b.user_id where u.user_name = 'rename_bvt_moved';
-- The same source label in a different account is resolved only within that account.
create account rename_bvt_tenant admin_name 'admin' identified by 'pw31460';
-- @session:id=4&user=rename_bvt_tenant:admin&password=pw31460
create user rename_bvt_moved identified by 'pw31460';
rename user rename_bvt_moved to rename_bvt_dst;
select count(*) from mo_catalog.mo_user where user_name = 'rename_bvt_dst';
select count(*) from mo_catalog.mo_user where user_name = 'rename_bvt_moved';
rename user admin to rename_bvt_end;
-- @session
select count(*) from mo_catalog.mo_user u join rename_user_bvt_db.identity_before b on u.user_id = b.user_id where u.user_name = 'rename_bvt_moved';
drop account rename_bvt_tenant;
drop user rename_bvt_src, rename_bvt_moved;
drop role rename_bvt_reader, rename_bvt_other, rename_bvt_extra;
drop database rename_user_bvt_db;
select count(*) from mo_catalog.mo_user where user_name in ('rename_bvt_src', 'rename_bvt_dst', 'rename_bvt_moved', 'rename_bvt_taken', 'rename_bvt_tmp', 'rename_bvt_end');
