
interface Table1 {
  id: number
  table2_id: number
  test: string
}

interface Table2 {
  id: number
  text2: number
}

interface Table2Join extends Table2 {
  on: {"id": "table2_id"}
}

interface Table3Join extends Table2 {
  on: "id2:table3_id,id4:table4"
}

interface Request1 {
  join: {
    [name: string]: Table2Join | Table3Join
  }
}


const r: Request1 = {
  join: {
    
    test: {
      on: "id2:table3_id,id4:table4"
      
    }
  }
}
