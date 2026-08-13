package rel

func (q *Query) IsReadOnly() bool {
	return q.EffectiveWrite().Mode == MODE_READONLY
}

func (q *Query) EffectiveWrite() Write {
	w := q.Write

	if w.Mode == MODE_READONLY && w.Relation.Name == "" && len(w.OnConflict) == 0 && len(w.InsertOnly) == 0 && len(w.UpdateOnly) == 0 {
		w = Write{
			Mode:       MODE_READONLY,
			Relation:   q.Relation,
			OnConflict: q.OnConflict,
			InsertOnly: q.InsertOnly,
			UpdateOnly: q.UpdateOnly,
		}
	}

	if w.Mode == MODE_READONLY && q.ParentQuery != nil {
		parent := q.ParentQuery.EffectiveWrite()
		if parent.Mode != MODE_READONLY {
			w.Mode = parent.Mode
			if w.Relation.Name == "" {
				w.Relation = q.Relation
			}
			if len(w.OnConflict) == 0 {
				w.OnConflict = parent.OnConflict
			}
			if len(w.InsertOnly) == 0 {
				w.InsertOnly = parent.InsertOnly
			}
			if len(w.UpdateOnly) == 0 {
				w.UpdateOnly = parent.UpdateOnly
			}
		}
	}

	return w
}
