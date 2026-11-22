// Pure helper for the call outcome editor: build the wrap-up PATCH body from the
// loaded baseline and the form, including only what changed. Present fields
// replace server-side, so sending untouched ones would overwrite concurrent edits.

export interface OutcomeBaseline {
  disposition?: { id: string } | null;
  notes?: string | null;
  tags?: { id: string }[];
}
export interface OutcomeForm {
  dispositionId: string;
  notes: string;
  tagIds: string[];
}
export interface OutcomePatch {
  disposition_id?: string | null;
  notes?: string | null;
  tag_ids?: string[];
}

export function outcomePatch(base: OutcomeBaseline, form: OutcomeForm): OutcomePatch {
  const out: OutcomePatch = {};
  const baseDisp = base.disposition?.id ?? "";
  if (form.dispositionId !== baseDisp) out.disposition_id = form.dispositionId || null;
  const baseNotes = base.notes ?? "";
  if (form.notes !== baseNotes) out.notes = form.notes === "" ? null : form.notes;
  const baseTags = new Set((base.tags ?? []).map((t) => t.id));
  const formTags = new Set(form.tagIds);
  if (baseTags.size !== formTags.size || [...formTags].some((id) => !baseTags.has(id))) {
    out.tag_ids = [...formTags];
  }
  return out;
}
