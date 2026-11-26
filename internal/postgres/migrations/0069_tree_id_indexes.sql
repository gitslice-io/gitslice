-- Speed up root_tree_id authorization checks for RepositoryService reads
-- (ReadFile, ReadFiles, ListDirectory, ResolvePath, Checkout) on both
-- published commits and draft changeset patchset trees.
create index if not exists idx_commits_root_tree_id
	on commits(root_tree_id);

create index if not exists idx_patchsets_result_tree_id
	on patchsets(result_tree_id)
	where result_tree_id is not null;

create index if not exists idx_patchsets_base_tree_id
	on patchsets(base_tree_id)
	where base_tree_id is not null;
