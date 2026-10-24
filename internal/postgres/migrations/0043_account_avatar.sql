-- An account's uploaded picture: the object store key of its normalized PNG,
-- or '' when the account shows the pixel picture generated from its name.
alter table accounts add column if not exists avatar_key text not null default '';
