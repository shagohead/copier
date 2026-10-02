package second

func (d *Receiver) CopyFromArgument(s *Argument) {
	d.SomeField = s.SomeField
	d.IntField = s.IntField
}

func (s *Receiver) CopyIntoArgument(d *Argument) {
	d.SomeField = s.SomeField
	d.IntField = s.IntField
}
