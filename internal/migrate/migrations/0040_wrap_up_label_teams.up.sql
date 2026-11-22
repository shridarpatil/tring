-- Per-team dispositions and tags. A label with no rows here is shared (offered
-- to every team); one with rows is offered only to calls wrapped up for one of
-- those teams.
CREATE TABLE disposition_teams (
    tenant_id      UUID NOT NULL,
    disposition_id UUID NOT NULL REFERENCES dispositions(id) ON DELETE CASCADE,
    team_name      TEXT NOT NULL,
    PRIMARY KEY (disposition_id, team_name),
    FOREIGN KEY (tenant_id, team_name) REFERENCES teams(tenant_id, name) ON DELETE CASCADE
);
CREATE INDEX disposition_teams_tenant_team ON disposition_teams (tenant_id, team_name);

CREATE TABLE call_tag_teams (
    tenant_id UUID NOT NULL,
    tag_id    UUID NOT NULL REFERENCES call_tags(id) ON DELETE CASCADE,
    team_name TEXT NOT NULL,
    PRIMARY KEY (tag_id, team_name),
    FOREIGN KEY (tenant_id, team_name) REFERENCES teams(tenant_id, name) ON DELETE CASCADE
);
CREATE INDEX call_tag_teams_tenant_team ON call_tag_teams (tenant_id, team_name);

-- The team whose ring reached the wrapping agent (NULL = teamless), which
-- picks the labels the wrap-up may use.
ALTER TABLE calls ADD COLUMN wrap_up_team TEXT;
