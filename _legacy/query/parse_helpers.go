package query

// /////////// helpers /////////////
func optParseUntilSeparatedBy(ctx *ParsingContext, close, separator string, parser func() error) error {

	// if ctx.lexer.ConsumeString(open) == nil {
	// 	return nil
	// }
	if ctx.lexer.ConsumeString(close) != nil {
		return nil
	}

	for {
		var last = ctx.lexer.last

		if err := parser(); err != nil {
			return err //errors.Wrap(err, "awaiting "+close+" separated by "+separator)
		}

		if last == ctx.lexer.last {
			return ctx.lexer.last.ErrorMessage("expected " + separator + " or " + close)
		}

		sep_found := false
		if ctx.lexer.ConsumeString(separator) != nil {
			sep_found = true
		}

		if ctx.lexer.ConsumeString(close) != nil {
			break
		} else {
			// We're allowing the separator to be right before the closing
			if sep_found {
				continue
			}
			return ctx.lexer.last.ErrorMessage("expected " + close)
		}
	}

	return nil
}
