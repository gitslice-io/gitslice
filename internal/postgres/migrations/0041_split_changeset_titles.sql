-- A changeset title is one line, like the subject of a commit message, and the
-- rest of a message is its description. Clients used to send a whole message as
-- the title, which lists and pages then showed as one long paragraph. This
-- splits the titles that hold several lines: the first line stays the title and
-- the rest goes in front of any description. The commit messages of landed
-- changesets are untouched.
update changesets
set title = btrim(split_part(title, E'\n', 1), E' \r\t'),
    description = case
        when btrim(description) = '' then btrim(substr(title, position(E'\n' in title) + 1), E' \n\r\t')
        else btrim(substr(title, position(E'\n' in title) + 1), E' \n\r\t') || E'\n\n' || description
    end
where position(E'\n' in title) > 0;
