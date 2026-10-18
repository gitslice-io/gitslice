-- A slice's description: a short note shown on its page, such as what it
-- holds or which project it mirrors (with credit). It is not part of the
-- slice definition, so changing it makes no new definition version.
alter table slices add column if not exists description text not null default '';
